package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	handlingClosedHours     = "closed_hours"
	handlingBurstSuppressed = "burst_suppressed"
	handlingSpamSuppressed  = "spam_suppressed"
	burstPerCaller          = "caller_burst"
	burstPerNumber          = "destination_burst"
	blockedCaller           = "caller_blocked"
)

type inboundBurstPolicy struct {
	WindowSeconds   int64
	PerCaller       int64
	PerNumber       int64
	CooldownSeconds int64
	TrustedNumbers  map[string]bool
	BlockedNumbers  map[string]bool
	BlockedPairs    map[string]bool
}

func boundedBurstSetting(config sdk.Config, key string, fallback, maximum int64) int64 {
	raw := strings.TrimSpace(config.Get(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 || parsed > maximum {
		return fallback
	}
	return parsed
}

func loadInboundBurstPolicy(config sdk.Config) inboundBurstPolicy {
	policy := inboundBurstPolicy{
		WindowSeconds:   boundedBurstSetting(config, "inbound_burst_window_seconds", 60, 3600),
		PerCaller:       boundedBurstSetting(config, "inbound_burst_per_caller", 12, 10000),
		PerNumber:       boundedBurstSetting(config, "inbound_burst_per_number", 60, 100000),
		CooldownSeconds: boundedBurstSetting(config, "inbound_burst_cooldown_seconds", 300, 86400),
		TrustedNumbers:  map[string]bool{},
		BlockedNumbers:  map[string]bool{},
		BlockedPairs:    map[string]bool{},
	}
	if policy.WindowSeconds == 0 {
		policy.WindowSeconds = 60
	}
	for _, number := range strings.Split(config.Get("inbound_burst_trusted_numbers"), ",") {
		if normalized := compactPhoneNumber(number); normalized != "" {
			policy.TrustedNumbers[normalized] = true
		}
	}
	for _, entry := range strings.Split(config.Get("inbound_spam_blocked_callers"), ",") {
		caller, destination, scoped := strings.Cut(strings.TrimSpace(entry), "@")
		caller, destination = strings.TrimSpace(caller), strings.TrimSpace(destination)
		if !validE164(caller) {
			continue
		}
		if scoped {
			if validE164(destination) {
				policy.BlockedPairs[compactPhoneNumber(caller)+"@"+compactPhoneNumber(destination)] = true
			}
		} else {
			policy.BlockedNumbers[compactPhoneNumber(caller)] = true
		}
	}
	return policy
}

func isSuppressedHandlingReason(reason string) bool {
	return reason == handlingBurstSuppressed || reason == handlingSpamSuppressed
}

// registerInboundAttempt serializes the decision in SQLite. The primary key
// makes carrier webhook retries free: only genuinely new call IDs count.
func (a *App) registerInboundAttempt(route *routeRow, carrierSID, from, to string, now time.Time, policy inboundBurstPolicy) (string, error) {
	a.burstMu.Lock()
	reason, destinationAlert, err := a.registerInboundAttemptLocked(route, carrierSID, from, to, now, policy)
	a.burstMu.Unlock()
	if destinationAlert && err == nil && globalCtx != nil {
		ctx := globalCtx.WithProject(route.ProjectID)
		ctx.Logger().Warn("inbound destination burst detected", "provider_call_id", carrierSID, "to", to)
		ctx.Emit("telephony.burst.detected", map[string]any{
			"provider_call_id": carrierSID, "to_number": to,
			"reason": burstPerNumber, "occurred_at": now.UTC().Format(time.RFC3339Nano),
		})
	}
	if reason == blockedCaller && err == nil && globalCtx != nil {
		ctx := globalCtx.WithProject(route.ProjectID)
		ctx.Logger().Warn("inbound caller blocked by displayed number", "provider_call_id", carrierSID, "to", to, "from", from)
		ctx.Emit("telephony.spam.suppressed", map[string]any{
			"provider_call_id": carrierSID, "to_number": to, "from_number": from,
			"reason": reason, "occurred_at": now.UTC().Format(time.RFC3339Nano),
		})
	}
	if reason != "" && reason != blockedCaller && err == nil && globalCtx != nil {
		ctx := globalCtx.WithProject(route.ProjectID)
		ctx.Logger().Warn("inbound burst suppressed", "provider_call_id", carrierSID, "to", to, "from", from, "reason", reason)
		ctx.Emit("telephony.burst.suppressed", map[string]any{
			"provider_call_id": carrierSID, "to_number": to, "from_number": from,
			"reason": reason, "occurred_at": now.UTC().Format(time.RFC3339Nano),
		})
	}
	return reason, err
}

func (a *App) registerInboundAttemptLocked(route *routeRow, carrierSID, from, to string, now time.Time, policy inboundBurstPolicy) (string, bool, error) {
	if route == nil || carrierSID == "" {
		return "", false, errors.New("inbound burst identity is unavailable")
	}
	if policy.PerCaller == 0 && policy.PerNumber == 0 && len(policy.BlockedNumbers) == 0 && len(policy.BlockedPairs) == 0 {
		return "", false, nil
	}
	to = compactPhoneNumber(to)
	from = compactPhoneNumber(from)
	if to == "" {
		return "", false, errors.New("inbound destination is unavailable")
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT OR IGNORE INTO inbound_burst_attempts
		(project_id,carrier_slug,carrier_connection_id,carrier_sid,to_number,from_number,received_at)
		VALUES(?,?,?,?,?,?,?)`, route.ProjectID, route.CarrierSlug, route.CarrierConnectionID, carrierSID, to, from, now.Unix())
	if err != nil {
		return "", false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if inserted == 0 {
		var existing string
		err = tx.QueryRow(`SELECT suppression_reason FROM inbound_burst_attempts WHERE project_id=? AND carrier_slug=? AND carrier_connection_id=? AND carrier_sid=?`,
			route.ProjectID, route.CarrierSlug, route.CarrierConnectionID, carrierSID).Scan(&existing)
		if err != nil {
			return "", false, err
		}
		return existing, false, tx.Commit()
	}
	if from != "" && (policy.BlockedNumbers[from] || policy.BlockedPairs[from+"@"+to]) {
		if _, err := tx.Exec(`UPDATE inbound_burst_attempts SET suppression_reason=? WHERE project_id=? AND carrier_slug=? AND carrier_connection_id=? AND carrier_sid=?`,
			blockedCaller, route.ProjectID, route.CarrierSlug, route.CarrierConnectionID, carrierSID); err != nil {
			return "", false, err
		}
		return blockedCaller, false, tx.Commit()
	}
	trusted := policy.TrustedNumbers[from]
	reason := ""
	if from != "" && !trusted && policy.PerCaller > 0 {
		var active string
		err = tx.QueryRow(`SELECT reason FROM inbound_burst_cooldowns WHERE project_id=? AND to_number=? AND from_number=? AND expires_at>?`,
			route.ProjectID, to, from, now.Unix()).Scan(&active)
		if err == nil {
			reason = active
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	destinationAlert := false
	if policy.PerNumber > 0 {
		var count int64
		err = tx.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM inbound_burst_attempts WHERE project_id=? AND to_number=? AND received_at>=? LIMIT ?)`,
			route.ProjectID, to, now.Unix()-policy.WindowSeconds+1, policy.PerNumber+1).Scan(&count)
		if err != nil {
			return "", false, err
		}
		if count > policy.PerNumber {
			var active string
			err = tx.QueryRow(`SELECT reason FROM inbound_burst_cooldowns WHERE project_id=? AND to_number=? AND from_number='*' AND expires_at>?`,
				route.ProjectID, to, now.Unix()).Scan(&active)
			if errors.Is(err, sql.ErrNoRows) {
				destinationAlert = true
				_, err = tx.Exec(`INSERT INTO inbound_burst_cooldowns(project_id,to_number,from_number,reason,expires_at)
					VALUES(?,?,'*',?,?) ON CONFLICT(project_id,to_number,from_number)
					DO UPDATE SET reason=excluded.reason,expires_at=excluded.expires_at`,
					route.ProjectID, to, burstPerNumber, now.Unix()+policy.CooldownSeconds)
			}
			if err != nil {
				return "", false, err
			}
		}
	}
	if reason == "" && policy.PerCaller > 0 && from != "" && !trusted {
		var count int64
		err = tx.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM inbound_burst_attempts WHERE project_id=? AND to_number=? AND from_number=? AND received_at>=? LIMIT ?)`,
			route.ProjectID, to, from, now.Unix()-policy.WindowSeconds+1, policy.PerCaller+1).Scan(&count)
		if err != nil {
			return "", false, err
		}
		if count > policy.PerCaller {
			reason = burstPerCaller
		}
	}
	if reason != "" {
		if _, err = tx.Exec(`INSERT INTO inbound_burst_cooldowns(project_id,to_number,from_number,reason,expires_at)
			VALUES(?,?,?,?,?) ON CONFLICT(project_id,to_number,from_number)
			DO UPDATE SET reason=excluded.reason,expires_at=MAX(expires_at,excluded.expires_at)`,
			route.ProjectID, to, from, reason, now.Unix()+policy.CooldownSeconds); err != nil {
			return "", false, err
		}
		if _, err = tx.Exec(`UPDATE inbound_burst_attempts SET suppression_reason=? WHERE project_id=? AND carrier_slug=? AND carrier_connection_id=? AND carrier_sid=?`,
			reason, route.ProjectID, route.CarrierSlug, route.CarrierConnectionID, carrierSID); err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit inbound burst decision: %w", err)
	}
	return reason, destinationAlert, nil
}

func routeHandlingReason(plan *inboundRoutingPlan) string {
	if plan == nil || plan.TerminalType != "hangup" {
		return ""
	}
	for _, step := range plan.Trace {
		if step.NodeType == "schedule" && step.Outcome == "closed" {
			return handlingClosedHours
		}
	}
	return ""
}

func planHasAnnouncement(plan *inboundRoutingPlan) bool {
	if plan == nil {
		return false
	}
	for _, step := range plan.Trace {
		if step.NodeType == "announcement" && step.Outcome == "play" {
			return true
		}
	}
	return false
}

func terminalAnnouncementText(plan *inboundRoutingPlan) string {
	if plan == nil || !planHasAnnouncement(plan) {
		return ""
	}
	var execution routingExecutionContext
	if json.Unmarshal([]byte(plan.ContextJSON), &execution) != nil {
		return ""
	}
	nodes := make(map[string]routingNode, len(execution.Definition.Nodes))
	for _, node := range execution.Definition.Nodes {
		nodes[node.ID] = node
	}
	parts := []string{}
	for _, step := range plan.Trace {
		if step.NodeType == "announcement" {
			if text := strings.TrimSpace(routingConfigString(nodes[step.NodeID].Config, "text")); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, " ")
}

// Telnyx reports answer and speech completion as separate callbacks. Never
// queue hangup with speak: doing so can cut off the announcement mid-sentence.
func terminalAnnouncementClientState(callID string) string {
	return base64.StdEncoding.EncodeToString([]byte("terminal-announcement:" + callID))
}

func (a *App) startTelnyxTerminalAnnouncement(ctx *sdk.AppCtx, row *callRow) error {
	if row != nil {
		fresh, err := a.db().findCall(row.ID)
		if err != nil {
			return err
		}
		row = fresh
	}
	if row == nil || row.AnnouncementState != "awaiting_answer" || isTerminalStatus(row.Status) {
		return nil
	}
	if row.AnsweredAt == "" && row.Status != "answered" {
		return errors.New("terminal announcement is waiting for carrier answer")
	}
	prompt := strings.TrimSpace(row.AnnouncementText)
	if prompt == "" {
		return errors.New("terminal announcement has no text")
	}
	// Store speaking only after command acceptance. If the process dies between
	// acceptance and persistence, replay uses the same carrier idempotency key.
	_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "speak_text", map[string]any{
		"call_control_id": row.CarrierSID, "payload": prompt, "payload_type": "text",
		"voice": "Telnyx.NaturalHD.Astra", "language": "fr-FR",
		"client_state": terminalAnnouncementClientState(row.ID),
		"command_id":   telnyxCommandID(row.ID, "terminal-announcement"),
	})
	if err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE calls SET announcement_state='speaking' WHERE id=? AND announcement_state='awaiting_answer' AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, row.ID)
	return err
}

func (a *App) finishTelnyxTerminalAnnouncement(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil || (row.AnnouncementState != "speaking" && row.AnnouncementState != "finishing") || isTerminalStatus(row.Status) {
		return nil
	}
	result, err := a.db().db.Exec(`UPDATE calls SET announcement_state='finishing' WHERE id=? AND announcement_state IN ('speaking','finishing') AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, row.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	_, err = executeCarrierTool(ctx, row.CarrierConnectionID, "hangup_call", map[string]any{
		"call_control_id": row.CarrierSID, "command_id": telnyxCommandID(row.ID, "terminal-announcement-hangup"),
	})
	if err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE calls SET announcement_state='finished' WHERE id=? AND announcement_state='finishing'`, row.ID)
	return err
}

func (a *App) completeTelnyxAnnouncementEvent(ctx *sdk.AppCtx, row *callRow) error {
	unlock := a.softphones.lockClaim(row.ID)
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	current, err := a.db().findCall(row.ID)
	if err != nil {
		return err
	}
	if !routingMayControlCall(current) || current.AnnouncementState == "finished" {
		return nil
	}
	if current.AnnouncementState != "speaking" && current.AnnouncementState != "finishing" {
		// Recover a completion delivered after a crash between carrier acceptance
		// of speak and saving the speaking state.
		var count int
		if err = a.db().db.QueryRow(`SELECT COUNT(*) FROM routing_effects WHERE call_id=? AND stage='speak' AND attempts>0`, row.ID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE calls SET announcement_state='finishing' WHERE id=? AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, row.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE routing_effects SET status='pending',attempts=CASE WHEN stage='hangup' THEN attempts ELSE 0 END,stage='hangup',next_attempt_at=? WHERE call_id=? AND status NOT IN ('done','canceled')`, ringTime(time.Now()), row.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var nodeID string
	if effectErr := a.db().db.QueryRow(`SELECT node_id FROM routing_effects WHERE call_id=? AND stage='hangup' AND status='pending' LIMIT 1`, row.ID).Scan(&nodeID); effectErr == nil {
		unlock()
		unlock = nil
		return a.driveRoutingEffect(ctx.WithProject(row.ProjectID), row.ID, nodeID)
	} else if !errors.Is(effectErr, sql.ErrNoRows) {
		return effectErr
	}
	current.AnnouncementState = "finishing"
	if err = a.finishTelnyxTerminalAnnouncement(ctx, current); err != nil {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE routing_effects SET status='done',updated_at=? WHERE call_id=? AND stage='hangup'`, ringTime(time.Now()), row.ID)
	return err
}

func (a *App) suppressInboundCall(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil || !isSuppressedHandlingReason(row.HandlingReason) {
		return nil
	}
	if ctx == nil {
		return errors.New("app context unavailable for inbound suppression")
	}
	plan := &inboundRoutingPlan{NodeID: "suppression", TerminalType: "reject"}
	if err := a.ensureRoutingEffect(row, plan); err != nil {
		return err
	}
	return a.driveRoutingEffect(ctx.WithProject(row.ProjectID), row.ID, plan.NodeID)
}

func (a *App) suppressTerminalRoutingCall(ctx *sdk.AppCtx, row *callRow) error {
	if ctx == nil {
		return errors.New("app context unavailable for terminal routing")
	}
	if err := a.terminateCarrierCall(ctx, row); err != nil {
		return err
	}

	return a.db().updateStatus(row.ID, "canceled", row.ErrorMessage)
}

func writeSuppressedTwilioCall(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<Response><Reject reason="rejected"/></Response>`))
}
