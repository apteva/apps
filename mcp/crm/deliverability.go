package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const softBounceQuarantineThreshold = 3

// ChannelDeliverability is Messaging-derived operational health. It is kept
// separate from Email Checker's verification result because a syntactically
// valid mailbox may still be unsubscribed, complained, or bouncing.
type ChannelDeliverability struct {
	Transport              string `json:"transport"`
	Status                 string `json:"status"`
	ConsecutiveSoftBounces int    `json:"consecutive_soft_bounces"`
	LastBounceAt           string `json:"last_bounce_at,omitempty"`
	LastDeliveredAt        string `json:"last_delivered_at,omitempty"`
	StatusReason           string `json:"status_reason,omitempty"`
	StatusUpdatedAt        string `json:"status_updated_at,omitempty"`
	Quarantined            bool   `json:"quarantined"`
	QuarantinedAt          string `json:"quarantined_at,omitempty"`
	Suppressed             bool   `json:"suppressed"`
	SuppressionKind        string `json:"suppression_kind,omitempty"`
	SuppressionMatch       string `json:"suppression_match,omitempty"`
	SuppressionReason      string `json:"suppression_reason,omitempty"`
	SuppressionSource      string `json:"suppression_source,omitempty"`
	SuppressedAt           string `json:"suppressed_at,omitempty"`
	SuppressionCheckedAt   string `json:"suppression_checked_at,omitempty"`
	Messageable            bool   `json:"messageable"`
	MessageabilityReason   string `json:"messageability_reason,omitempty"`
}

func (s *ChannelDeliverability) deriveMessageability() {
	s.Messageable = true
	s.MessageabilityReason = ""
	switch {
	case s.Suppressed:
		s.Messageable = false
		s.MessageabilityReason = "suppressed"
		if s.SuppressionReason != "" {
			s.MessageabilityReason = "suppressed: " + s.SuppressionReason
		}
	case s.Quarantined:
		s.Messageable = false
		s.MessageabilityReason = "quarantined after repeated transient bounces"
	case s.Status == "hard_bounced":
		s.Messageable = false
		s.MessageabilityReason = "hard bounced"
	case s.Status == "complained":
		s.Messageable = false
		s.MessageabilityReason = "recipient complained"
	case s.Status == "unsubscribed":
		s.Messageable = false
		s.MessageabilityReason = "unsubscribed"
	}
}

func loadChannelDeliverability(db *sql.DB, pid string, channelID int64) ([]ChannelDeliverability, error) {
	rows, err := db.Query(
		`SELECT transport, status, consecutive_soft_bounces,
			COALESCE(last_bounce_at,''), COALESCE(last_delivered_at,''),
			COALESCE(status_reason,''), COALESCE(status_updated_at,''),
			quarantined, COALESCE(quarantined_at,''), suppressed,
			COALESCE(suppression_kind,''), COALESCE(suppression_match,''),
			COALESCE(suppression_reason,''), COALESCE(suppression_source,''),
			COALESCE(suppressed_at,''), COALESCE(suppression_checked_at,'')
		 FROM contact_channel_delivery_state
		 WHERE project_id = ? AND channel_id = ?
		 ORDER BY CASE transport WHEN 'email' THEN 0 WHEN 'sms' THEN 1 ELSE 2 END`,
		pid, channelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChannelDeliverability{}
	for rows.Next() {
		var state ChannelDeliverability
		var quarantined, suppressed int
		if err := rows.Scan(
			&state.Transport, &state.Status, &state.ConsecutiveSoftBounces,
			&state.LastBounceAt, &state.LastDeliveredAt, &state.StatusReason,
			&state.StatusUpdatedAt, &quarantined, &state.QuarantinedAt, &suppressed,
			&state.SuppressionKind, &state.SuppressionMatch, &state.SuppressionReason,
			&state.SuppressionSource, &state.SuppressedAt, &state.SuppressionCheckedAt,
		); err != nil {
			return nil, err
		}
		state.Quarantined = quarantined != 0
		state.Suppressed = suppressed != 0
		state.deriveMessageability()
		out = append(out, state)
	}
	return out, rows.Err()
}

func (a *App) deliverabilityWorkers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "messaging-suppression-reconcile",
		Schedule: "@every 5m",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			return a.reconcileMessagingSuppressions(ctx)
		},
	}}
}

func eventProjectID(ctx *sdk.AppCtx, event sdk.Event) (string, error) {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" && ctx != nil {
		pid = strings.TrimSpace(ctx.CurrentProject())
	}
	if pid == "" {
		return "", fmt.Errorf("%s missing project_id", event.Name())
	}
	return pid, nil
}

func messagingEventID(event sdk.Event) string {
	if id := strings.TrimSpace(anyString(event.Data["event_id"])); id != "" {
		return id
	}
	body, _ := json.Marshal(struct {
		Name string         `json:"name"`
		Data map[string]any `json:"data"`
	}{Name: event.Name(), Data: event.Data})
	digest := sha256.Sum256(body)
	return "derived:" + hex.EncodeToString(digest[:16])
}

func insertProcessedMessagingEvent(tx *sql.Tx, pid string, event sdk.Event) (bool, error) {
	res, err := tx.Exec(
		`INSERT OR IGNORE INTO crm_processed_messaging_events
			(project_id, source_install_id, event_id) VALUES (?, ?, ?)`,
		pid, event.SourceInstallID, messagingEventID(event),
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	return affected > 0, err
}

func transportChannelKind(transport string) string {
	if transport == channelEmail {
		return "email"
	}
	if transport == channelSMS || transport == channelWhatsApp {
		return "phone"
	}
	return ""
}

func canonicalDeliveryRecipient(transport, recipient string) string {
	kind := transportChannelKind(transport)
	if kind == "" {
		return ""
	}
	return normaliseChannel(kind, recipient)
}

type deliveryStateRow struct {
	ChannelID              int64
	Status                 string
	ConsecutiveSoftBounces int
	StatusUpdatedAt        string
	Suppressed             bool
	SuppressionReason      string
	SuppressionSource      string
	SuppressionKind        string
	SuppressionMatch       string
}

func getDeliveryStateTx(tx *sql.Tx, pid string, channelID int64, transport string) (*deliveryStateRow, error) {
	row := &deliveryStateRow{ChannelID: channelID}
	var suppressed int
	err := tx.QueryRow(
		`SELECT status, consecutive_soft_bounces, COALESCE(status_updated_at,''), suppressed,
			COALESCE(suppression_reason,''), COALESCE(suppression_source,''),
			COALESCE(suppression_kind,''), COALESCE(suppression_match,'')
		 FROM contact_channel_delivery_state
		 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
		pid, channelID, transport,
	).Scan(&row.Status, &row.ConsecutiveSoftBounces, &row.StatusUpdatedAt, &suppressed,
		&row.SuppressionReason, &row.SuppressionSource, &row.SuppressionKind, &row.SuppressionMatch)
	if err != nil {
		return nil, err
	}
	row.Suppressed = suppressed != 0
	return row, nil
}

func parseEventTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func eventIsOlder(incoming, current string) bool {
	in, inOK := parseEventTime(incoming)
	cur, curOK := parseEventTime(current)
	return inOK && curOK && in.Before(cur)
}

func (a *App) handleMessagingDeliveryEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	pid, err := eventProjectID(ctx, event)
	if err != nil {
		return err
	}
	transport := strings.ToLower(strings.TrimSpace(anyString(event.Data["channel"])))
	kind := strings.ToLower(strings.TrimSpace(anyString(event.Data["kind"])))
	recipient := canonicalDeliveryRecipient(transport, anyString(event.Data["recipient"]))
	channelKind := transportChannelKind(transport)
	if channelKind == "" || recipient == "" {
		return nil
	}
	occurred := strings.TrimSpace(anyString(event.Data["occurred_at"]))
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339Nano)
	}
	reason := strings.TrimSpace(anyString(event.Data["reason"]))

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := insertProcessedMessagingEvent(tx, pid, event)
	if err != nil || !inserted {
		return err
	}
	var channelID, contactID int64
	err = tx.QueryRow(
		`SELECT id, contact_id FROM contact_channels
		 WHERE project_id = ? AND kind = ? AND value = ? LIMIT 1`,
		pid, channelKind, recipient,
	).Scan(&channelID, &contactID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	state, err := getDeliveryStateTx(tx, pid, channelID, transport)
	if err != nil {
		return err
	}
	if eventIsOlder(occurred, state.StatusUpdatedAt) {
		return tx.Commit()
	}

	needsThresholdSuppression := false
	needsThresholdRemoval := false
	switch kind {
	case "delivered":
		wasSoft := state.Status == "soft_bounced"
		_, err = tx.Exec(
			`UPDATE contact_channel_delivery_state
			 SET status = CASE WHEN status = 'soft_bounced' THEN 'active' ELSE status END,
				 consecutive_soft_bounces = 0,
				 last_delivered_at = ?,
				 status_reason = CASE WHEN status = 'soft_bounced' THEN NULL ELSE status_reason END,
				 quarantined = 0, quarantined_at = NULL,
				 status_updated_at = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
			occurred, occurred, pid, channelID, transport,
		)
		needsThresholdRemoval = wasSoft && state.Suppressed && state.SuppressionSource == "crm" && state.SuppressionReason == "soft-bounce-threshold"
	case "bounced":
		permanent := boolFromAny(event.Data["permanent"])
		if permanent {
			if reason == "" {
				reason = "hard-bounce"
			}
			_, err = tx.Exec(
				`UPDATE contact_channel_delivery_state
				 SET status = 'hard_bounced', last_bounce_at = ?, status_reason = ?,
					 status_updated_at = ?, updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				occurred, reason, occurred, pid, channelID, transport,
			)
		} else if state.Status == "active" || state.Status == "soft_bounced" {
			count := state.ConsecutiveSoftBounces + 1
			quarantine := count >= softBounceQuarantineThreshold
			if reason == "" {
				reason = "transient-bounce"
			}
			_, err = tx.Exec(
				`UPDATE contact_channel_delivery_state
				 SET status = 'soft_bounced', consecutive_soft_bounces = ?, last_bounce_at = ?,
					 status_reason = ?, status_updated_at = ?, quarantined = ?,
					 quarantined_at = CASE WHEN ? = 1 THEN COALESCE(quarantined_at, ?) ELSE quarantined_at END,
					 updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				count, occurred, reason, occurred, boolToInt(quarantine), boolToInt(quarantine), occurred,
				pid, channelID, transport,
			)
			needsThresholdSuppression = quarantine && !state.Suppressed
		}
	case "complained":
		if reason == "" {
			reason = "complaint"
		}
		_, err = tx.Exec(
			`UPDATE contact_channel_delivery_state
			 SET status = 'complained', status_reason = ?, status_updated_at = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
			reason, occurred, pid, channelID, transport,
		)
	default:
		// Open/click/delay and provider-specific informational events do not
		// change whether the address is safe to message.
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	emitChannelDeliverabilityChanged(ctx, pid, contactID, channelID, transport)
	if needsThresholdSuppression {
		a.addSoftBounceSuppression(ctx, pid, transport, recipient)
	}
	if needsThresholdRemoval {
		a.removeSoftBounceSuppression(ctx, pid, transport, state.SuppressionKind, state.SuppressionMatch)
	}
	return nil
}

type suppressionCheckResult struct {
	Suppressed   bool   `json:"suppressed"`
	Reason       string `json:"reason"`
	Source       string `json:"source"`
	Kind         string `json:"kind"`
	Matched      string `json:"matched"`
	SuppressedAt string `json:"suppressed_at"`
}

func suppressionCheckDetailed(ctx *sdk.AppCtx, pid, transport, address string) (suppressionCheckResult, error) {
	var out suppressionCheckResult
	err := callMessagingTool(ctx, "suppression_check", map[string]any{
		"_project_id": pid,
		"channel":     transport,
		"address":     address,
	}, &out)
	return out, err
}

func (a *App) addSoftBounceSuppression(ctx *sdk.AppCtx, pid, transport, address string) {
	var out map[string]any
	if err := callMessagingTool(ctx, "suppression_add", map[string]any{
		"_project_id": pid,
		"channel":     transport,
		"kind":        "address",
		"address":     address,
		"reason":      "soft-bounce-threshold",
		"source":      "crm",
	}, &out); err != nil {
		ctx.Logger().Warn("crm soft-bounce suppression will be retried", "project_id", pid, "channel", transport, "address", address, "err", err)
	}
}

func (a *App) removeSoftBounceSuppression(ctx *sdk.AppCtx, pid, transport, kind, address string) {
	if kind == "" {
		kind = "address"
	}
	if address == "" {
		return
	}
	var out map[string]any
	if err := callMessagingTool(ctx, "suppression_remove", map[string]any{
		"_project_id": pid,
		"channel":     transport,
		"kind":        kind,
		"address":     address,
	}, &out); err != nil {
		ctx.Logger().Warn("crm soft-bounce suppression removal will be retried", "project_id", pid, "channel", transport, "address", address, "err", err)
	}
}

type affectedDeliveryRoute struct {
	ChannelID int64
	ContactID int64
	Transport string
	Address   string
}

func suppressionAffectedRoutes(db *sql.DB, pid, transport, kind, address string) ([]affectedDeliveryRoute, error) {
	channelKind := transportChannelKind(transport)
	if channelKind == "" {
		return nil, nil
	}
	query := `SELECT c.id, c.contact_id, s.transport, c.value
		FROM contact_channels c
		JOIN contact_channel_delivery_state s
		  ON s.project_id = c.project_id AND s.channel_id = c.id
		WHERE c.project_id = ? AND c.kind = ? AND s.transport = ?`
	args := []any{pid, channelKind, transport}
	if kind == "domain" {
		query += ` AND LOWER(SUBSTR(c.value, INSTR(c.value, '@') + 1)) = ?`
		args = append(args, strings.ToLower(strings.TrimPrefix(address, "@")))
	} else {
		query += ` AND c.value = ?`
		args = append(args, canonicalDeliveryRecipient(transport, address))
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []affectedDeliveryRoute{}
	for rows.Next() {
		var route affectedDeliveryRoute
		if err := rows.Scan(&route.ChannelID, &route.ContactID, &route.Transport, &route.Address); err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

func statusForSuppression(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(reason, "complaint"):
		return "complained"
	case strings.Contains(reason, "unsubscribe"), strings.Contains(reason, "stop"), strings.Contains(reason, "opt-out"):
		return "unsubscribed"
	case strings.Contains(reason, "hard-bounce"), strings.Contains(reason, "hard bounce"):
		return "hard_bounced"
	default:
		return ""
	}
}

func (a *App) handleMessagingSuppressionEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	pid, err := eventProjectID(ctx, event)
	if err != nil {
		return err
	}
	transport := strings.ToLower(strings.TrimSpace(anyString(event.Data["channel"])))
	kind := strings.ToLower(strings.TrimSpace(anyString(event.Data["kind"])))
	if kind == "" {
		kind = "address"
	}
	address := strings.TrimSpace(anyString(event.Data["address"]))
	if transportChannelKind(transport) == "" || address == "" {
		return nil
	}
	routes, err := suppressionAffectedRoutes(ctx.AppDB(), pid, transport, kind, address)
	if err != nil {
		return err
	}
	operation := strings.ToLower(strings.TrimSpace(anyString(event.Data["operation"])))
	suppressed := boolFromAny(event.Data["suppressed"])
	if operation == "remove" {
		suppressed = false
	}
	checks := map[int64]suppressionCheckResult{}
	if !suppressed {
		for _, route := range routes {
			check, err := suppressionCheckDetailed(ctx, pid, route.Transport, route.Address)
			if err != nil {
				return err
			}
			checks[route.ChannelID] = check
		}
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := insertProcessedMessagingEvent(tx, pid, event)
	if err != nil || !inserted {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	reason := strings.TrimSpace(anyString(event.Data["reason"]))
	source := strings.TrimSpace(anyString(event.Data["source"]))
	suppressedAt := strings.TrimSpace(anyString(event.Data["first_seen"]))
	if suppressedAt == "" {
		suppressedAt = now
	}
	for _, route := range routes {
		if suppressed {
			status := statusForSuppression(reason)
			_, err = tx.Exec(
				`UPDATE contact_channel_delivery_state
				 SET suppressed = 1, suppression_kind = ?, suppression_match = ?,
					 suppression_reason = ?, suppression_source = ?, suppressed_at = ?,
					 suppression_checked_at = ?,
					 status = CASE WHEN ? <> '' THEN ? ELSE status END,
					 status_reason = CASE WHEN ? <> '' THEN ? ELSE status_reason END,
					 status_updated_at = CASE WHEN ? <> '' THEN ? ELSE status_updated_at END,
					 updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				kind, address, reason, source, suppressedAt, now,
				status, status, status, reason, status, now, pid, route.ChannelID, route.Transport,
			)
		} else {
			check := checks[route.ChannelID]
			if check.Suppressed {
				_, err = tx.Exec(
					`UPDATE contact_channel_delivery_state
					 SET suppressed = 1, suppression_kind = ?, suppression_match = ?,
						 suppression_reason = ?, suppression_source = ?, suppressed_at = ?,
						 suppression_checked_at = ?, updated_at = CURRENT_TIMESTAMP
					 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
					check.Kind, check.Matched, check.Reason, check.Source, nullStr(check.SuppressedAt), now,
					pid, route.ChannelID, route.Transport,
				)
			} else {
				_, err = tx.Exec(
					`UPDATE contact_channel_delivery_state
					 SET suppressed = 0, suppression_kind = NULL, suppression_match = NULL,
						 suppression_reason = NULL, suppression_source = NULL, suppressed_at = NULL,
						 suppression_checked_at = ?,
						 status = CASE WHEN status IN ('hard_bounced','complained','unsubscribed') THEN 'active' ELSE status END,
						 status_reason = CASE WHEN status IN ('hard_bounced','complained','unsubscribed') THEN NULL ELSE status_reason END,
						 status_updated_at = ?, updated_at = CURRENT_TIMESTAMP
					 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
					now, now, pid, route.ChannelID, route.Transport,
				)
			}
		}
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, route := range routes {
		emitChannelDeliverabilityChanged(ctx, pid, route.ContactID, route.ChannelID, route.Transport)
	}
	return nil
}

func emitChannelDeliverabilityChanged(ctx *sdk.AppCtx, pid string, contactID, channelID int64, transport string) {
	if ctx == nil || contactID == 0 || channelID == 0 {
		return
	}
	states, err := loadChannelDeliverability(ctx.AppDB(), pid, channelID)
	if err != nil {
		return
	}
	for _, state := range states {
		if state.Transport != transport {
			continue
		}
		ctx.Emit("contact.channel.deliverability.changed", map[string]any{
			"contact_id":            contactID,
			"channel_id":            channelID,
			"transport":             transport,
			"status":                state.Status,
			"suppressed":            state.Suppressed,
			"quarantined":           state.Quarantined,
			"messageable":           state.Messageable,
			"messageability_reason": state.MessageabilityReason,
		})
		return
	}
}

type messagingSuppression struct {
	Channel   string `json:"channel"`
	Kind      string `json:"kind"`
	Address   string `json:"address"`
	Reason    string `json:"reason"`
	Source    string `json:"source"`
	FirstSeen string `json:"first_seen"`
}

func listMessagingSuppressions(ctx *sdk.AppCtx, pid string) ([]messagingSuppression, error) {
	all := []messagingSuppression{}
	for offset := 0; ; offset += 1000 {
		var page struct {
			Suppressions []messagingSuppression `json:"suppressions"`
			HasMore      bool                   `json:"has_more"`
		}
		if err := callMessagingTool(ctx, "suppression_list", map[string]any{
			"_project_id": pid,
			"limit":       1000,
			"offset":      offset,
		}, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Suppressions...)
		if !page.HasMore || len(page.Suppressions) == 0 {
			return all, nil
		}
	}
}

func crmProjectIDs(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT project_id FROM contacts ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		out = append(out, pid)
	}
	return out, rows.Err()
}

type reconcileRoute struct {
	ChannelID int64
	Transport string
	Address   string
}

func allDeliveryRoutes(db *sql.DB, pid string) ([]reconcileRoute, error) {
	rows, err := db.Query(
		`SELECT c.id, s.transport, c.value
		 FROM contact_channels c
		 JOIN contact_channel_delivery_state s
		   ON s.project_id = c.project_id AND s.channel_id = c.id
		 WHERE c.project_id = ? AND c.kind IN ('email','phone')`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []reconcileRoute{}
	for rows.Next() {
		var route reconcileRoute
		if err := rows.Scan(&route.ChannelID, &route.Transport, &route.Address); err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

func effectiveSuppression(items []messagingSuppression, transport, address string) *messagingSuppression {
	canonical := canonicalDeliveryRecipient(transport, address)
	domain := ""
	if at := strings.LastIndex(canonical, "@"); at >= 0 {
		domain = strings.ToLower(canonical[at+1:])
	}
	for i := range items {
		item := &items[i]
		if item.Channel != transport {
			continue
		}
		if item.Kind == "address" && canonicalDeliveryRecipient(transport, item.Address) == canonical {
			return item
		}
	}
	for i := range items {
		item := &items[i]
		if item.Channel == transport && item.Kind == "domain" && strings.EqualFold(strings.TrimPrefix(item.Address, "@"), domain) {
			return item
		}
	}
	return nil
}

func (a *App) reconcileMessagingSuppressions(ctx *sdk.AppCtx) error {
	if messagingBound(ctx) == nil {
		return nil
	}
	pids, err := crmProjectIDs(ctx.AppDB())
	if err != nil {
		return err
	}
	var firstErr error
	for _, pid := range pids {
		if err := a.reconcileProjectSuppressions(ctx, pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) reconcileProjectSuppressions(ctx *sdk.AppCtx, pid string) error {
	items, err := listMessagingSuppressions(ctx, pid)
	if err != nil {
		return err
	}
	routes, err := allDeliveryRoutes(ctx.AppDB(), pid)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, route := range routes {
		match := effectiveSuppression(items, route.Transport, route.Address)
		if match != nil {
			status := statusForSuppression(match.Reason)
			_, err = tx.Exec(
				`UPDATE contact_channel_delivery_state
				 SET suppressed = 1, suppression_kind = ?, suppression_match = ?,
					 suppression_reason = ?, suppression_source = ?, suppressed_at = ?,
					 suppression_checked_at = ?,
					 status = CASE WHEN ? <> '' THEN ? ELSE status END,
					 status_reason = CASE WHEN ? <> '' THEN ? ELSE status_reason END,
					 status_updated_at = CASE WHEN ? <> '' THEN ? ELSE status_updated_at END,
					 updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				match.Kind, match.Address, match.Reason, match.Source, nullStr(match.FirstSeen), now,
				status, status, status, match.Reason, status, now, pid, route.ChannelID, route.Transport,
			)
		} else {
			_, err = tx.Exec(
				`UPDATE contact_channel_delivery_state
				 SET suppressed = 0, suppression_kind = NULL, suppression_match = NULL,
					 suppression_reason = NULL, suppression_source = NULL, suppressed_at = NULL,
					 suppression_checked_at = ?,
					 status = CASE WHEN status IN ('hard_bounced','complained','unsubscribed') THEN 'active' ELSE status END,
					 status_reason = CASE WHEN status IN ('hard_bounced','complained','unsubscribed') THEN NULL ELSE status_reason END,
					 updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				now, pid, route.ChannelID, route.Transport,
			)
		}
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// These calls are intentionally outside the transaction. The state row is
	// the retry marker, so a temporary Messaging failure needs no outbox table.
	rows, err := ctx.AppDB().Query(
		`SELECT c.value, s.transport, COALESCE(s.suppression_kind,''), COALESCE(s.suppression_match,''),
			s.quarantined, s.suppressed, s.consecutive_soft_bounces,
			COALESCE(s.suppression_source,''), COALESCE(s.suppression_reason,'')
		 FROM contact_channel_delivery_state s
		 JOIN contact_channels c ON c.project_id = s.project_id AND c.id = s.channel_id
		 WHERE s.project_id = ? AND (
			(s.quarantined = 1 AND s.suppressed = 0) OR
			(s.quarantined = 0 AND s.consecutive_soft_bounces = 0 AND s.suppressed = 1
			 AND s.suppression_source = 'crm' AND s.suppression_reason = 'soft-bounce-threshold')
		 )`, pid)
	if err != nil {
		return err
	}
	type retryRoute struct {
		address, transport, kind, match, source, reason string
		quarantined, suppressed, count                  int
	}
	retries := []retryRoute{}
	for rows.Next() {
		var route retryRoute
		if err := rows.Scan(&route.address, &route.transport, &route.kind, &route.match,
			&route.quarantined, &route.suppressed, &route.count, &route.source, &route.reason); err != nil {
			rows.Close()
			return err
		}
		retries = append(retries, route)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, route := range retries {
		if route.quarantined != 0 && route.suppressed == 0 {
			a.addSoftBounceSuppression(ctx, pid, route.transport, route.address)
		} else {
			a.removeSoftBounceSuppression(ctx, pid, route.transport, route.kind, route.match)
		}
	}
	return nil
}

// syncUncheckedContactSuppressions performs the proposal's one-time
// suppression_check for newly added routes. Failures remain unchecked and are
// healed by the periodic full-list reconciliation.
func syncUncheckedContactSuppressions(ctx *sdk.AppCtx, pid string, contactID int64) {
	if messagingBound(ctx) == nil {
		return
	}
	rows, err := ctx.AppDB().Query(
		`SELECT c.id, c.value, s.transport
		 FROM contact_channels c
		 JOIN contact_channel_delivery_state s
		   ON s.project_id = c.project_id AND s.channel_id = c.id
		 WHERE c.project_id = ? AND c.contact_id = ? AND s.suppression_checked_at IS NULL`,
		pid, contactID)
	if err != nil {
		return
	}
	routes := []affectedDeliveryRoute{}
	for rows.Next() {
		var route affectedDeliveryRoute
		if err := rows.Scan(&route.ChannelID, &route.Address, &route.Transport); err == nil {
			routes = append(routes, route)
		}
	}
	rows.Close()
	for _, route := range routes {
		check, err := suppressionCheckDetailed(ctx, pid, route.Transport, route.Address)
		if err != nil {
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if check.Suppressed {
			status := statusForSuppression(check.Reason)
			_, _ = ctx.AppDB().Exec(
				`UPDATE contact_channel_delivery_state
				 SET suppressed = 1, suppression_kind = ?, suppression_match = ?, suppression_reason = ?,
					 suppression_source = ?, suppressed_at = ?, suppression_checked_at = ?,
					 status = CASE WHEN ? <> '' THEN ? ELSE status END,
					 status_reason = CASE WHEN ? <> '' THEN ? ELSE status_reason END,
					 status_updated_at = CASE WHEN ? <> '' THEN ? ELSE status_updated_at END,
					 updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				check.Kind, check.Matched, check.Reason, check.Source, nullStr(check.SuppressedAt), now,
				status, status, status, check.Reason, status, now,
				pid, route.ChannelID, route.Transport)
		} else {
			_, _ = ctx.AppDB().Exec(
				`UPDATE contact_channel_delivery_state
				 SET suppressed = 0, suppression_checked_at = ?, updated_at = CURRENT_TIMESTAMP
				 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
				now, pid, route.ChannelID, route.Transport)
		}
	}
}

func recordSuppressionPreflight(db *sql.DB, pid string, channelID int64, transport string, check suppressionCheckResult) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if check.Suppressed {
		status := statusForSuppression(check.Reason)
		_, _ = db.Exec(
			`UPDATE contact_channel_delivery_state
			 SET suppressed = 1, suppression_kind = ?, suppression_match = ?, suppression_reason = ?,
				 suppression_source = ?, suppressed_at = ?, suppression_checked_at = ?,
				 status = CASE WHEN ? <> '' THEN ? ELSE status END,
				 status_reason = CASE WHEN ? <> '' THEN ? ELSE status_reason END,
				 status_updated_at = CASE WHEN ? <> '' THEN ? ELSE status_updated_at END,
				 updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
			check.Kind, check.Matched, check.Reason, check.Source, nullStr(check.SuppressedAt), now,
			status, status, status, check.Reason, status, now,
			pid, channelID, transport)
		return
	}
	_, _ = db.Exec(
		`UPDATE contact_channel_delivery_state
		 SET suppressed = 0, suppression_kind = NULL, suppression_match = NULL,
			 suppression_reason = NULL, suppression_source = NULL, suppressed_at = NULL,
			 suppression_checked_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND channel_id = ? AND transport = ?`,
		now, pid, channelID, transport)
}
