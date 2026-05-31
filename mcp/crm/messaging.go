// CRM ↔ messaging coupling.
//
// CRM is the agent's single interface for talking to people. This
// file wires it to the optional messaging app: outbound via
// contacts_send_message / contacts_reply, inbound via
// messaging_inbound_receive (plus legacy POST /inbound), and
// conversation threading on top.
//
// The messaging dependency is soft: every entry point gates on
// ctx.IntegrationFor("messaging") and returns a clear error when it
// isn't bound. Without messaging installed CRM remains a perfectly
// usable contact store.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	channelEmail    = "email"
	channelSMS      = "sms"
	channelWhatsApp = "whatsapp"
)

// ─── Conversation type + DB helpers ────────────────────────────────

type Conversation struct {
	ID              int64  `json:"id"`
	ContactID       int64  `json:"contact_id"`
	Channel         string `json:"channel"`
	Subject         string `json:"subject,omitempty"`
	RootMessageID   string `json:"root_message_id,omitempty"`
	Status          string `json:"status"`   // open | pending | closed | spam
	Priority        string `json:"priority"` // low | normal | high | urgent
	StatusChangedAt string `json:"status_changed_at,omitempty"`
	StartedAt       string `json:"started_at"`
	LastActivityAt  string `json:"last_activity_at"`
}

// Conversation status + priority vocabularies. Kept small and explicit —
// see migrations/005 for why we don't carry snooze / on-hold / solved.
var convoStatuses = map[string]bool{"open": true, "pending": true, "closed": true, "spam": true}
var convoPriorities = map[string]bool{"low": true, "normal": true, "high": true, "urgent": true}

// convoColumns is the canonical SELECT list for a Conversation row.
// Every reader scans in this exact order via scanConversation.
const convoColumns = `id, contact_id, channel,
	COALESCE(subject,''), COALESCE(root_message_id,''),
	COALESCE(status,'open'), COALESCE(priority,'normal'),
	COALESCE(status_changed_at,''),
	started_at, last_activity_at`

func scanConversation(row interface{ Scan(...any) error }) (*Conversation, error) {
	c := &Conversation{}
	err := row.Scan(&c.ID, &c.ContactID, &c.Channel, &c.Subject, &c.RootMessageID,
		&c.Status, &c.Priority, &c.StatusChangedAt, &c.StartedAt, &c.LastActivityAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func dbConversationCreate(tx *sql.Tx, pid string, contactID int64, channel, subject, rootMsgID, when string) (int64, error) {
	res, err := tx.Exec(
		`INSERT INTO contact_conversations
			(project_id, contact_id, channel, subject, root_message_id,
			 started_at, last_activity_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, contactID, channel, nullStr(subject), nullStr(rootMsgID), when, when,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// dbConversationForChannel returns the persistent (contact, channel)
// conversation for sms/whatsapp. Email always creates a new thread.
func dbConversationForChannel(db *sql.DB, pid string, contactID int64, channel string) (*Conversation, error) {
	if channel == channelEmail {
		return nil, nil
	}
	row := db.QueryRow(
		`SELECT `+convoColumns+`
		 FROM contact_conversations
		 WHERE project_id = ? AND contact_id = ? AND channel = ?
		 ORDER BY id ASC LIMIT 1`,
		pid, contactID, channel,
	)
	c, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func dbConversationByRootMsgID(db *sql.DB, pid, rootMsgID string) (*Conversation, error) {
	if rootMsgID == "" {
		return nil, nil
	}
	row := db.QueryRow(
		`SELECT `+convoColumns+`
		 FROM contact_conversations
		 WHERE project_id = ? AND root_message_id = ? LIMIT 1`,
		pid, rootMsgID,
	)
	c, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dbConversationByActivityMsgID resolves a conversation by finding an
// activity with the given Message-Id header. Used when an inbound's
// In-Reply-To points at a specific outbound rather than the chain root.
func dbConversationByActivityMsgID(db *sql.DB, pid string, contactID int64, msgIDHeader string) (int64, error) {
	if msgIDHeader == "" {
		return 0, nil
	}
	row := db.QueryRow(
		`SELECT conversation_id FROM contact_activities
		 WHERE project_id = ? AND contact_id = ?
		   AND message_id_header = ? AND conversation_id IS NOT NULL
		 ORDER BY id DESC LIMIT 1`,
		pid, contactID, msgIDHeader,
	)
	var id int64
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}

func dbConversationsList(db *sql.DB, pid string, contactID int64, channel, status string, limit int) ([]*Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "project_id = ? AND contact_id = ?"
	args := []any{pid, contactID}
	if channel != "" {
		where += " AND channel = ?"
		args = append(args, channel)
	}
	if status != "" {
		where += " AND COALESCE(status,'open') = ?"
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT `+convoColumns+`
		 FROM contact_conversations
		 WHERE `+where+`
		 ORDER BY last_activity_at DESC LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func dbConversationGet(db *sql.DB, pid string, id int64) (*Conversation, error) {
	row := db.QueryRow(
		`SELECT `+convoColumns+`
		 FROM contact_conversations
		 WHERE project_id = ? AND id = ?`,
		pid, id,
	)
	c, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dbConversationSetStatus patches status and/or priority on a
// conversation. Empty string for either means "leave unchanged".
// status_changed_at is stamped only when status actually moves.
// Returns rows affected (0 = conversation not in this project).
func dbConversationSetStatus(db *sql.DB, pid string, id int64, status, priority string) (int64, error) {
	sets := []string{}
	args := []any{}
	if status != "" {
		sets = append(sets, "status = ?", "status_changed_at = ?")
		args = append(args, status, time.Now().UTC().Format(time.RFC3339))
	}
	if priority != "" {
		sets = append(sets, "priority = ?")
		args = append(args, priority)
	}
	if len(sets) == 0 {
		return 0, errors.New("nothing to update")
	}
	args = append(args, pid, id)
	res, err := db.Exec(
		`UPDATE contact_conversations SET `+strings.Join(sets, ", ")+
			` WHERE project_id = ? AND id = ?`,
		args...,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// dbConversationReopenIfClosed flips a non-open conversation back to
// 'open' — called when a new inbound arrives on an existing thread. The
// contact replied, so the ball is back in our court. Spam conversations
// stay spam: inbound spam should not make the thread actionable again.
// No-op (0 rows) on a conversation that's already open, so it's always
// safe to call. Returns true when it actually reopened something.
func dbConversationReopenIfClosed(db *sql.DB, pid string, id int64) (bool, error) {
	res, err := db.Exec(
		`UPDATE contact_conversations
		 SET status = 'open', status_changed_at = ?
		 WHERE project_id = ? AND id = ?
		   AND COALESCE(status,'open') NOT IN ('open', 'spam')`,
		time.Now().UTC().Format(time.RFC3339), pid, id,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbContactMarkSpam(db *sql.DB, pid string, contactID int64) error {
	if _, err := db.Exec(
		`UPDATE contacts
		 SET status = 'spam', updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ?`,
		pid, contactID,
	); err != nil {
		return err
	}
	return dbAddTag(db, pid, contactID, "spam")
}

func dbContactIsSpam(db *sql.DB, pid string, contactID int64) (bool, error) {
	var status string
	if err := db.QueryRow(
		`SELECT COALESCE(status,'active')
		 FROM contacts
		 WHERE project_id = ? AND id = ? AND deleted_at IS NULL`,
		pid, contactID,
	).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if strings.EqualFold(status, "spam") {
		return true, nil
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(1) FROM contact_tags
		 WHERE project_id = ? AND contact_id = ? AND LOWER(tag_name) = 'spam'`,
		pid, contactID,
	).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// dbConversationActivities returns activities for a conversation in
// chronological order. ID is the tiebreaker so two messages timestamped
// identically render in insertion order — fixes the audit's stability
// concern for the conversation view.
func dbConversationActivities(db *sql.DB, pid string, conversationID int64) ([]*Activity, error) {
	rows, err := db.Query(
		`SELECT id, contact_id, kind, body, occurred_at, COALESCE(source,''),
				COALESCE(conversation_id, 0)
		 FROM contact_activities
		 WHERE project_id = ? AND conversation_id = ?
		 ORDER BY occurred_at ASC, id ASC`,
		pid, conversationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Activity{}
	for rows.Next() {
		a := &Activity{}
		if err := rows.Scan(&a.ID, &a.ContactID, &a.Kind, &a.Body, &a.OccurredAt, &a.Source, &a.ConversationID); err == nil {
			out = append(out, a)
		}
	}
	return out, nil
}

// ─── Atomic activity insert with conversation linkage ──────────────

type logMessageActivityInput struct {
	ProjectID       string
	ContactID       int64
	Kind            string
	Body            string
	OccurredAt      string
	Source          string
	SourceDetail    map[string]any
	ConversationID  int64
	MessageIDHeader string // outbound: Message-Id we sent; inbound: Message-Id we received
	MessagingID     int64  // messaging-app row id; used for inbound dedup
}

func logMessageActivity(db *sql.DB, in logMessageActivityInput) (*Activity, error) {
	if in.OccurredAt == "" {
		in.OccurredAt = time.Now().UTC().Format(time.RFC3339)
	}
	var sdJSON []byte
	if len(in.SourceDetail) > 0 {
		sdJSON, _ = json.Marshal(in.SourceDetail)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var convoArg, sdArg, msgIDArg, messagingIDArg any
	if in.ConversationID > 0 {
		convoArg = in.ConversationID
	}
	if len(sdJSON) > 0 {
		sdArg = string(sdJSON)
	}
	if in.MessageIDHeader != "" {
		msgIDArg = in.MessageIDHeader
	}
	if in.MessagingID > 0 {
		messagingIDArg = in.MessagingID
	}

	res, err := tx.Exec(
		`INSERT INTO contact_activities
			(project_id, contact_id, kind, body, occurred_at, source,
			 source_detail, conversation_id, message_id_header, messaging_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, in.ContactID, in.Kind, in.Body, in.OccurredAt, in.Source,
		sdArg, convoArg, msgIDArg, messagingIDArg,
	)
	if err != nil {
		// Inbound dedup: messaging may retry the same delivery. The
		// UNIQUE index on (project_id, messaging_id) bounces the second
		// insert; surface it as a typed sentinel so the caller can
		// short-circuit cleanly.
		if in.MessagingID > 0 && isUniqueViolation(err) {
			return nil, errDuplicateMessagingID
		}
		return nil, err
	}
	aid, _ := res.LastInsertId()
	if _, err := tx.Exec(
		`UPDATE contacts SET last_contact_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		in.OccurredAt, in.ContactID, in.ProjectID,
	); err != nil {
		return nil, err
	}
	if in.ConversationID > 0 {
		if _, err := tx.Exec(
			`UPDATE contact_conversations SET last_activity_at = ?
			 WHERE id = ? AND project_id = ?`,
			in.OccurredAt, in.ConversationID, in.ProjectID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Activity{
		ID: aid, ContactID: in.ContactID, Kind: in.Kind, Body: in.Body,
		OccurredAt: in.OccurredAt, Source: in.Source,
		ConversationID: in.ConversationID,
	}, nil
}

var errDuplicateMessagingID = errors.New("duplicate messaging_id (inbound already logged)")

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// modernc.org/sqlite formats UNIQUE failures as
	// "constraint failed: UNIQUE constraint failed: ..."
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "constraint failed")
}

// ─── Address resolution ───────────────────────────────────────────

type resolvedAddress struct {
	Channel string
	Address string
}

// resolveContactAddress picks (channel, address) for a send.
//
//   - preferChannel non-empty → primary_<channel>, fall through to
//     contact_channels of the matching kind, error if none.
//   - empty → channel of the contact's most-recent message activity;
//     final fallback is precedence email > sms > whatsapp.
//   - Ambiguous (no prior activity, multiple kinds available) →
//     "channel required" error listing the options.
func resolveContactAddress(db *sql.DB, pid string, c *Contact, preferChannel string) (*resolvedAddress, error) {
	if c == nil {
		return nil, errors.New("contact required")
	}
	pickFromChannels := func(channel string) string {
		switch channel {
		case channelEmail:
			if c.PrimaryEmail != "" {
				return c.PrimaryEmail
			}
		case channelSMS, channelWhatsApp:
			if c.PrimaryPhone != "" {
				return c.PrimaryPhone
			}
		}
		kind := contactChannelKindFor(channel)
		if kind == "" {
			return ""
		}
		var v string
		row := db.QueryRow(
			`SELECT value FROM contact_channels
			 WHERE project_id = ? AND contact_id = ? AND kind = ?
			 ORDER BY is_primary DESC, id ASC LIMIT 1`,
			pid, c.ID, kind,
		)
		_ = row.Scan(&v)
		return v
	}

	if preferChannel != "" {
		addr := pickFromChannels(preferChannel)
		if addr == "" {
			return nil, fmt.Errorf("contact has no %s address", preferChannel)
		}
		return &resolvedAddress{Channel: preferChannel, Address: addr}, nil
	}

	row := db.QueryRow(
		`SELECT kind FROM contact_activities
		 WHERE project_id = ? AND contact_id = ?
		   AND kind IN ('email_sent','email_received',
						'sms_sent','sms_received',
						'whatsapp_sent','whatsapp_received')
		 ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		pid, c.ID,
	)
	var kind string
	var lastChannel string
	if err := row.Scan(&kind); err == nil {
		switch {
		case strings.HasPrefix(kind, "email_"):
			lastChannel = channelEmail
		case strings.HasPrefix(kind, "sms_"):
			lastChannel = channelSMS
		case strings.HasPrefix(kind, "whatsapp_"):
			lastChannel = channelWhatsApp
		}
	}
	if lastChannel != "" {
		if addr := pickFromChannels(lastChannel); addr != "" {
			return &resolvedAddress{Channel: lastChannel, Address: addr}, nil
		}
	}

	available := []string{}
	for _, ch := range []string{channelEmail, channelSMS, channelWhatsApp} {
		if pickFromChannels(ch) != "" {
			available = append(available, ch)
		}
	}
	switch len(available) {
	case 0:
		return nil, errors.New("contact has no email or phone address")
	case 1:
		return &resolvedAddress{Channel: available[0], Address: pickFromChannels(available[0])}, nil
	default:
		return nil, fmt.Errorf("channel required: contact has %s — pass channel arg", strings.Join(available, " and "))
	}
}

// ─── Messaging client wrappers ────────────────────────────────────

func messagingBound(ctx *sdk.AppCtx) *sdk.BoundIntegration {
	if ctx == nil {
		return nil
	}
	return ctx.IntegrationFor("messaging")
}

func callMessagingSend(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("messaging", "send_message", args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func callMessagingTool(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	if messagingBound(ctx) == nil {
		return errors.New("messaging app not bound to CRM")
	}
	return ctx.PlatformAPI().CallAppResult("messaging", tool, args, out)
}

const crmMessagingInboundTool = "messaging_inbound_receive"

func ensureMessagingInboundRoutes(ctx *sdk.AppCtx, pid string) error {
	if messagingBound(ctx) == nil {
		return nil
	}
	for _, channel := range []string{channelEmail, channelSMS, channelWhatsApp} {
		var out map[string]any
		err := callMessagingTool(ctx, "inbound_route_set", map[string]any{
			"_project_id":  pid,
			"channel":      channel,
			"pattern":      "*",
			"target_app":   "crm",
			"target_route": crmMessagingInboundTool,
			"priority":     0,
		}, &out)
		if err != nil {
			return fmt.Errorf("%s route: %w", channel, err)
		}
	}
	return nil
}

// suppressionCheck calls messaging.suppression_check. Returns
// (suppressed, reason). Older messaging versions without this tool
// are treated as not-suppressed — messaging itself will still pre-flight
// against its own suppression list inside send_message.
func suppressionCheck(ctx *sdk.AppCtx, channel, address string) (bool, string) {
	var out struct {
		Suppressed bool   `json:"suppressed"`
		Reason     string `json:"reason"`
	}
	err := ctx.PlatformAPI().CallAppResult("messaging", "suppression_check",
		map[string]any{"channel": channel, "address": address}, &out)
	if err != nil {
		return false, ""
	}
	return out.Suppressed, out.Reason
}

type spamSuppressionResult struct {
	Attempted bool   `json:"attempted"`
	Kind      string `json:"kind,omitempty"`
	Address   string `json:"address,omitempty"`
	Error     string `json:"error,omitempty"`
}

func conversationSpamTarget(db *sql.DB, pid string, convo *Conversation, scope string) (kind, address string, err error) {
	if convo == nil {
		return "", "", errors.New("conversation not found")
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "sender"
	}
	if scope != "sender" && scope != "domain" {
		return "", "", fmt.Errorf("invalid spam_scope %q (sender|domain)", scope)
	}
	addr := ""
	row := db.QueryRow(
		`SELECT address
		 FROM conversation_participants
		 WHERE project_id = ? AND conversation_id = ? AND role = 'from'
		 ORDER BY CASE WHEN contact_id = ? THEN 0 ELSE 1 END, id DESC
		 LIMIT 1`,
		pid, convo.ID, convo.ContactID,
	)
	if err := row.Scan(&addr); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if strings.TrimSpace(addr) == "" {
		contact, err := dbGetByID(db, pid, convo.ContactID)
		if err != nil {
			return "", "", err
		}
		if contact != nil {
			switch convo.Channel {
			case channelEmail:
				addr = contact.PrimaryEmail
			case channelSMS, channelWhatsApp:
				addr = contact.PrimaryPhone
			}
		}
	}
	addr = canonicalParticipantAddress(convo.Channel, addr)
	if scope == "domain" {
		if convo.Channel != channelEmail {
			return "", "", errors.New("spam_scope=domain is only supported for email conversations")
		}
		domain := domainOf(addr)
		if domain == "" {
			return "", "", errors.New("email sender domain not found")
		}
		return "domain", domain, nil
	}
	if addr == "" {
		return "", "", nil
	}
	return "address", addr, nil
}

func applyConversationSpam(ctx *sdk.AppCtx, pid string, convo *Conversation, scope string, force bool) spamSuppressionResult {
	res := spamSuppressionResult{}
	if ctx == nil || convo == nil {
		return res
	}
	db := ctx.AppDB()
	if err := dbContactMarkSpam(db, pid, convo.ContactID); err != nil {
		res.Error = "mark contact spam: " + err.Error()
		return res
	}
	_, _ = logMessageActivity(db, logMessageActivityInput{
		ProjectID:      pid,
		ContactID:      convo.ContactID,
		Kind:           ActivityKindSystem,
		Body:           "marked conversation as spam",
		Source:         "crm",
		ConversationID: convo.ID,
	})
	kind, address, err := conversationSpamTarget(db, pid, convo, scope)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if address == "" || messagingBound(ctx) == nil {
		return res
	}
	res.Attempted = true
	res.Kind = kind
	res.Address = address
	var out map[string]any
	err = callMessagingTool(ctx, "suppression_add", map[string]any{
		"_project_id": pid,
		"channel":     convo.Channel,
		"address":     address,
		"kind":        kind,
		"reason":      "crm-spam",
		"source":      "crm",
		"force":       force,
	}, &out)
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func (a *App) toolMessagingSendersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	call := map[string]any{
		"_project_id":   pid,
		"verified_only": boolArg(args, "verified_only", true),
	}
	if channel := strings.TrimSpace(strArg(args, "channel")); channel != "" {
		call["channel"] = channel
	}
	var out map[string]any
	if err := callMessagingTool(ctx, "senders_list", call, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) toolMessagingTemplatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	channel := strings.TrimSpace(strArg(args, "channel"))
	if channel == "" {
		channel = channelWhatsApp
	}
	approvedOnly := boolArg(args, "approved_only", channel == channelWhatsApp)
	call := map[string]any{
		"_project_id": pid,
		"channel":     channel,
		"limit":       intArg(args, "limit", 200),
	}
	var out map[string]any
	if err := callMessagingTool(ctx, "template_list", call, &out); err != nil {
		return nil, err
	}
	if approvedOnly {
		if rows, ok := out["templates"].([]any); ok {
			filtered := make([]any, 0, len(rows))
			for _, row := range rows {
				m, _ := row.(map[string]any)
				if m == nil {
					continue
				}
				if !strings.EqualFold(anyString(m["channel"]), channel) {
					continue
				}
				status := strings.ToLower(strings.TrimSpace(anyString(m["provider_status"])))
				providerID := strings.TrimSpace(anyString(m["provider_template_id"]))
				if channel == channelWhatsApp && (providerID == "" || status != "approved") {
					continue
				}
				filtered = append(filtered, row)
			}
			out["templates"] = filtered
			out["count"] = len(filtered)
		}
	}
	return out, nil
}

func (a *App) toolMessagingWhatsAppSessionCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	to, err := a.resolveWhatsAppSessionRecipient(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	from, err := a.resolveWhatsAppSessionSender(ctx, pid, strArg(args, "from"))
	if err != nil {
		return nil, err
	}
	return a.checkWhatsAppSession(ctx, pid, from, to)
}

func (a *App) resolveWhatsAppSessionRecipient(ctx *sdk.AppCtx, pid string, args map[string]any) (string, error) {
	if to := cleanMessagingAddress(strArg(args, "to")); to != "" {
		return to, nil
	}
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	if cid == 0 {
		return "", errors.New("to or id/contact_id required")
	}
	c, err := dbGetByID(ctx.AppDB(), pid, cid)
	if err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("contact not found")
	}
	addr, err := resolveContactAddress(ctx.AppDB(), pid, c, channelWhatsApp)
	if err != nil {
		return "", err
	}
	return cleanMessagingAddress(addr.Address), nil
}

func (a *App) resolveWhatsAppSessionSender(ctx *sdk.AppCtx, pid, raw string) (string, error) {
	if from := cleanMessagingAddress(raw); from != "" {
		return from, nil
	}
	if from := cleanMessagingAddress(defaultSenderForChannel(ctx, channelWhatsApp)); from != "" {
		return from, nil
	}
	var out struct {
		Senders []struct {
			Channel   string `json:"channel"`
			Address   string `json:"address"`
			IsDefault bool   `json:"is_default"`
		} `json:"senders"`
	}
	err := callMessagingTool(ctx, "senders_list", map[string]any{
		"_project_id":   pid,
		"channel":       channelWhatsApp,
		"verified_only": true,
	}, &out)
	if err != nil {
		return "", err
	}
	for _, s := range out.Senders {
		if s.Channel == channelWhatsApp && s.IsDefault && cleanMessagingAddress(s.Address) != "" {
			return cleanMessagingAddress(s.Address), nil
		}
	}
	for _, s := range out.Senders {
		if s.Channel == channelWhatsApp && cleanMessagingAddress(s.Address) != "" {
			return cleanMessagingAddress(s.Address), nil
		}
	}
	return "", errors.New("no verified WhatsApp sender found in bound Messaging app")
}

func (a *App) checkWhatsAppSession(ctx *sdk.AppCtx, pid, from, to string) (map[string]any, error) {
	from = cleanMessagingAddress(from)
	to = cleanMessagingAddress(to)
	if from == "" || to == "" {
		return nil, errors.New("from and to required")
	}
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	var out struct {
		Messages []struct {
			From             string   `json:"from"`
			To               []string `json:"to"`
			MatchedRecipient string   `json:"matched_recipient"`
			ReceivedAt       string   `json:"received_at"`
			CreatedAt        string   `json:"created_at"`
		} `json:"messages"`
	}
	err := callMessagingTool(ctx, "message_list", map[string]any{
		"_project_id": pid,
		"direction":   "in",
		"channel":     channelWhatsApp,
		"address":     to,
		"since":       since,
		"limit":       50,
	}, &out)
	if err != nil {
		return nil, err
	}
	active := false
	lastInbound := ""
	for _, m := range out.Messages {
		if cleanMessagingAddress(m.From) != to {
			continue
		}
		matched := cleanMessagingAddress(m.MatchedRecipient) == from
		for _, recipient := range m.To {
			if cleanMessagingAddress(recipient) == from {
				matched = true
				break
			}
		}
		if matched {
			active = true
			if m.ReceivedAt != "" {
				lastInbound = m.ReceivedAt
			} else {
				lastInbound = m.CreatedAt
			}
			break
		}
	}
	return map[string]any{
		"active":       active,
		"from":         from,
		"to":           to,
		"since":        since,
		"last_inbound": lastInbound,
	}, nil
}

func anyString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func emitCRMEvent(ctx *sdk.AppCtx, pid, topic string, payload map[string]any) {
	if ctx == nil {
		return
	}
	if strings.TrimSpace(pid) == "" {
		ctx.Logger().Warn("crm emit without project", "topic", topic)
		ctx.Emit(topic, payload)
		return
	}
	ctx.EmitWithProject(topic, pid, payload)
}

// ─── Tool: contacts_send_message ──────────────────────────────────

func (a *App) toolSendMessage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.sendMessageImpl(ctx, args, false)
}

func (a *App) toolSendTest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.sendMessageImpl(ctx, args, true)
}

func (a *App) sendMessageImpl(ctx *sdk.AppCtx, args map[string]any, isTest bool) (any, error) {
	if messagingBound(ctx) == nil {
		return nil, errors.New("messaging app not bound to CRM: open CRM in the dashboard → Bindings → bind the messaging install to the 'messaging' role. (The app may already be installed in the project; binding is a separate explicit step.)")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := ensureMessagingInboundRoutes(ctx, pid); err != nil {
		ctx.Logger().Warn("crm messaging inbound route auto-wire failed", "project_id", pid, "err", err)
	}
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	if cid == 0 {
		return nil, errors.New("id (contact id) required")
	}
	body, _ := args["body"].(string)
	templateID := int64Arg(args, "template_id")
	contentSID := strArg(args, "content_sid")
	if body == "" && templateID == 0 && contentSID == "" {
		return nil, errors.New("body or template_id required")
	}

	c, err := dbGetByID(ctx.AppDB(), pid, cid)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("contact not found")
	}

	preferChannel := strings.ToLower(strings.TrimSpace(strArg(args, "channel")))
	addr, err := resolveContactAddress(ctx.AppDB(), pid, c, preferChannel)
	if err != nil {
		return nil, err
	}

	if !isTest {
		if suppressed, reason := suppressionCheck(ctx, addr.Channel, addr.Address); suppressed {
			return nil, fmt.Errorf("address suppressed (%s): %s", reason, addr.Address)
		}
	}

	// Sender resolution precedence: explicit `from` arg > list default
	// (when list_id supplied) > install-level default config.
	from := strArg(args, "from")
	listID := int64Arg(args, "list_id")
	var resolvedList *List
	if from == "" && listID != 0 {
		l, err := dbListGet(ctx.AppDB(), pid, listID)
		if err != nil {
			return nil, fmt.Errorf("list lookup: %w", err)
		}
		if l == nil {
			return nil, fmt.Errorf("list_id %d not found", listID)
		}
		resolvedList = l
		from = l.defaultSenderForChannel(addr.Channel)
	}
	if from == "" {
		from = defaultSenderForChannel(ctx, addr.Channel)
	}
	if from == "" {
		hint := map[string]string{channelEmail: "email", channelSMS: "phone", channelWhatsApp: "phone"}[addr.Channel]
		if listID != 0 {
			return nil, fmt.Errorf("from required (list_id %d has no default_sender_%s, and no install default configured)", listID, hint)
		}
		return nil, fmt.Errorf("from required (no default_sender_%s configured)", hint)
	}
	_ = resolvedList // reserved for future "tag activity with list" enrichment

	sendArgs := map[string]any{
		"_project_id": pid,
		"channel":     addr.Channel,
		"to":          addr.Address,
		"body":        body,
		"from":        from,
	}
	if subj := strArg(args, "subject"); subj != "" {
		sendArgs["subject"] = subj
	}
	if bodyHTML := strArg(args, "body_html"); bodyHTML != "" {
		sendArgs["body_html"] = bodyHTML
	}
	if templateID != 0 {
		sendArgs["template_id"] = templateID
	}
	if contentSID != "" {
		sendArgs["content_sid"] = contentSID
	}
	if idem := strArg(args, "idempotency_key"); idem != "" {
		sendArgs["idempotency_key"] = idem
	}
	if vars, ok := args["template_vars"].(map[string]any); ok {
		sendArgs["vars"] = vars
	} else if vars, ok := args["vars"].(map[string]any); ok {
		sendArgs["vars"] = vars
	}

	// Conversation linkage. caller-supplied conversation_id wins; else
	// for sms/whatsapp use the persistent per-channel conversation; for
	// email new sends, a fresh conversation is created post-send when we
	// know the outbound Message-Id.
	convoID := int64Arg(args, "conversation_id")
	var convo *Conversation
	if convoID > 0 {
		convo, err = dbConversationGet(ctx.AppDB(), pid, convoID)
		if err != nil {
			return nil, err
		}
		if convo == nil || convo.ContactID != cid {
			return nil, errors.New("conversation_id does not belong to this contact")
		}
		if convo.Channel == channelEmail && convo.RootMessageID != "" {
			sendArgs["in_reply_to"] = convo.RootMessageID
			sendArgs["headers"] = map[string]any{
				"In-Reply-To": convo.RootMessageID,
				"References":  convo.RootMessageID,
			}
		}
	} else if !isTest && (addr.Channel == channelSMS || addr.Channel == channelWhatsApp) {
		existing, err := dbConversationForChannel(ctx.AppDB(), pid, cid, addr.Channel)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			convo = existing
		} else {
			tx, err := ctx.AppDB().Begin()
			if err != nil {
				return nil, err
			}
			now := time.Now().UTC().Format(time.RFC3339)
			id, err := dbConversationCreate(tx, pid, cid, addr.Channel, "", "", now)
			if err != nil {
				tx.Rollback()
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			convo, _ = dbConversationGet(ctx.AppDB(), pid, id)
		}
	}

	resp, sendErr := callMessagingSend(ctx, sendArgs)

	if sendErr != nil {
		if !isTest {
			_, _ = logMessageActivity(ctx.AppDB(), logMessageActivityInput{
				ProjectID: pid,
				ContactID: cid,
				Kind:      failedKindForChannel(addr.Channel),
				Body:      truncate(body, 4000),
				Source:    "messaging",
				SourceDetail: map[string]any{
					"to":    addr.Address,
					"error": sendErr.Error(),
				},
			})
		}
		return nil, fmt.Errorf("messaging.send_message: %w", sendErr)
	}

	providerMsgID, _ := resp["provider_message_id"].(string)
	msgIDF, _ := resp["id"].(float64)
	msgID := int64(msgIDF)

	// New email thread → create the conversation now, rooted at the
	// outbound provider Message-Id.
	if !isTest && convo == nil && addr.Channel == channelEmail {
		tx, err := ctx.AppDB().Begin()
		if err == nil {
			now := time.Now().UTC().Format(time.RFC3339)
			subj := strArg(args, "subject")
			id, err := dbConversationCreate(tx, pid, cid, channelEmail, subj, providerMsgID, now)
			if err != nil {
				tx.Rollback()
			} else if err := tx.Commit(); err == nil {
				convo, _ = dbConversationGet(ctx.AppDB(), pid, id)
			}
		}
	}

	kind := sentKindForChannel(addr.Channel)
	if isTest {
		kind = testSentKindForChannel(addr.Channel)
	}
	activityBody := body
	if activityBody == "" && templateID != 0 {
		activityBody = fmt.Sprintf("(template #%d)", templateID)
	}
	if subj := strArg(args, "subject"); subj != "" && addr.Channel == channelEmail {
		activityBody = subj + "\n\n" + body
	}
	var convoIDForLog int64
	if convo != nil && !isTest {
		convoIDForLog = convo.ID
	}
	act, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{
		ProjectID: pid,
		ContactID: cid,
		Kind:      kind,
		Body:      truncate(activityBody, 4000),
		Source:    "messaging",
		SourceDetail: map[string]any{
			"messaging_id":        msgID,
			"provider_message_id": providerMsgID,
			"from":                from,
			"to":                  addr.Address,
			"test":                isTest,
		},
		ConversationID:  convoIDForLog,
		MessageIDHeader: providerMsgID,
	})
	if err != nil {
		return nil, err
	}
	if convoIDForLog != 0 {
		_ = dbConversationParticipantsAdd(ctx.AppDB(), pid, convoIDForLog, addr.Channel, []conversationParticipant{
			{Role: "from", Address: from},
			{Role: "to", Address: addr.Address, ContactID: cid},
		})
	}
	ctx.Emit("contact.activity.added", map[string]any{
		"contact_id":  cid,
		"activity_id": act.ID,
		"kind":        kind,
	})

	return map[string]any{
		"activity":            act,
		"channel":             addr.Channel,
		"to":                  addr.Address,
		"messaging_id":        msgID,
		"provider_message_id": providerMsgID,
		"conversation_id":     convoIDForLog,
	}, nil
}

// ─── Tool: contacts_reply ─────────────────────────────────────────

func (a *App) toolReply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if messagingBound(ctx) == nil {
		return nil, errors.New("messaging app not bound to CRM: open CRM in the dashboard → Bindings → bind the messaging install to the 'messaging' role. (The app may already be installed in the project; binding is a separate explicit step.)")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	if cid == 0 {
		return nil, errors.New("id required")
	}
	body, _ := args["body"].(string)
	if body == "" && int64Arg(args, "template_id") == 0 {
		return nil, errors.New("body or template_id required")
	}

	convoID := int64Arg(args, "conversation_id")
	var convo *Conversation
	if convoID > 0 {
		convo, err = dbConversationGet(ctx.AppDB(), pid, convoID)
		if err != nil {
			return nil, err
		}
		if convo == nil || convo.ContactID != cid {
			return nil, errors.New("conversation_id does not belong to this contact")
		}
	} else {
		row := ctx.AppDB().QueryRow(
			`SELECT conversation_id FROM contact_activities
			 WHERE project_id = ? AND contact_id = ?
			   AND kind IN ('email_received','sms_received','whatsapp_received')
			   AND conversation_id IS NOT NULL
			 ORDER BY occurred_at DESC, id DESC LIMIT 1`,
			pid, cid,
		)
		var lastConvoID int64
		if err := row.Scan(&lastConvoID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("no inbound conversation found; use contacts_send_message instead")
			}
			return nil, err
		}
		convo, err = dbConversationGet(ctx.AppDB(), pid, lastConvoID)
		if err != nil || convo == nil {
			return nil, errors.New("conversation lookup failed")
		}
	}

	sendArgs := map[string]any{
		"id":              cid,
		"channel":         convo.Channel,
		"body":            body,
		"conversation_id": convo.ID,
	}
	if from := strArg(args, "from"); from != "" {
		sendArgs["from"] = from
	}
	if templateID := int64Arg(args, "template_id"); templateID != 0 {
		sendArgs["template_id"] = templateID
	}
	if vars, ok := args["template_vars"].(map[string]any); ok {
		sendArgs["template_vars"] = vars
	} else if vars, ok := args["vars"].(map[string]any); ok {
		sendArgs["vars"] = vars
	}
	if subj := strArg(args, "subject"); subj != "" {
		sendArgs["subject"] = subj
	} else if convo.Channel == channelEmail && convo.Subject != "" {
		sendArgs["subject"] = "Re: " + strings.TrimPrefix(convo.Subject, "Re: ")
	}
	if pidArg := strArg(args, "_project_id"); pidArg != "" {
		sendArgs["_project_id"] = pidArg
	}
	return a.sendMessageImpl(ctx, sendArgs, false)
}

// ─── Tool: contacts_list_messageable ──────────────────────────────

func (a *App) toolListMessageable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	channel := strings.ToLower(strings.TrimSpace(strArg(args, "channel")))
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	where := []string{"project_id = ?", "deleted_at IS NULL", "(status IS NULL OR status = 'active')"}
	qargs := []any{pid}
	switch channel {
	case channelEmail:
		where = append(where, "primary_email IS NOT NULL AND primary_email <> ''")
	case channelSMS, channelWhatsApp:
		where = append(where, "primary_phone IS NOT NULL AND primary_phone <> ''")
	case "":
		where = append(where, "((primary_email IS NOT NULL AND primary_email <> '') OR (primary_phone IS NOT NULL AND primary_phone <> ''))")
	}
	// Exclude automated / no-reply senders from bulk-message audiences by
	// default — you rarely want to blast noreply@…. Individual sends and
	// explicit segments are unaffected; pass include_automated=true to
	// override.
	if includeAutomated, _ := args["include_automated"].(bool); !includeAutomated {
		where = append(where, `NOT EXISTS (SELECT 1 FROM contact_tags t
			WHERE t.project_id = contacts.project_id AND t.contact_id = contacts.id AND t.tag_name = ?)`)
		qargs = append(qargs, tagAutomated)
	}
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(
		`SELECT id, COALESCE(display_name,''), COALESCE(primary_email,''),
				COALESCE(primary_phone,'')
		 FROM contacts WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC LIMIT ?`,
		qargs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type listRow struct {
		ID           int64  `json:"id"`
		DisplayName  string `json:"display_name,omitempty"`
		PrimaryEmail string `json:"primary_email,omitempty"`
		PrimaryPhone string `json:"primary_phone,omitempty"`
	}
	out := []listRow{}
	for rows.Next() {
		var r listRow
		if err := rows.Scan(&r.ID, &r.DisplayName, &r.PrimaryEmail, &r.PrimaryPhone); err == nil {
			out = append(out, r)
		}
	}
	return map[string]any{"contacts": out, "count": len(out)}, nil
}

// ─── Tool: contacts_list_conversations / get_conversation ─────────

func (a *App) toolListConversations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	if cid == 0 {
		return nil, errors.New("id required")
	}
	channel := strings.ToLower(strArg(args, "channel"))
	status := strings.ToLower(strings.TrimSpace(strArg(args, "status")))
	if status != "" && !convoStatuses[status] {
		return nil, fmt.Errorf("invalid status %q (open|pending|closed|spam)", status)
	}
	limit := intArg(args, "limit", 50)
	out, err := dbConversationsList(ctx.AppDB(), pid, cid, channel, status, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversations": out, "count": len(out)}, nil
}

// ─── Tool: contacts_set_conversation_status ───────────────────────

func (a *App) toolSetConversationStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	convoID := int64Arg(args, "conversation_id")
	if convoID == 0 {
		return nil, errors.New("conversation_id required")
	}
	status := strings.ToLower(strings.TrimSpace(strArg(args, "status")))
	if status != "" && !convoStatuses[status] {
		return nil, fmt.Errorf("invalid status %q (open|pending|closed|spam)", status)
	}
	priority := strings.ToLower(strings.TrimSpace(strArg(args, "priority")))
	if priority != "" && !convoPriorities[priority] {
		return nil, fmt.Errorf("invalid priority %q (low|normal|high|urgent)", priority)
	}
	if status == "" && priority == "" {
		return nil, errors.New("status or priority required")
	}

	convo, err := dbConversationGet(ctx.AppDB(), pid, convoID)
	if err != nil {
		return nil, err
	}
	if convo == nil {
		return nil, errors.New("conversation not found")
	}
	// Optional contact-id safety check, matching get_conversation.
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	if cid != 0 && convo.ContactID != cid {
		return nil, errors.New("conversation does not belong to this contact")
	}

	if _, err := dbConversationSetStatus(ctx.AppDB(), pid, convoID, status, priority); err != nil {
		return nil, err
	}
	updated, err := dbConversationGet(ctx.AppDB(), pid, convoID)
	if err != nil {
		return nil, err
	}
	var suppression spamSuppressionResult
	if status == "spam" {
		suppression = applyConversationSpam(ctx, pid, updated, strArg(args, "spam_scope"), boolArg(args, "force", false))
	}
	ctx.Emit("conversation.status.changed", map[string]any{
		"conversation_id": convoID,
		"contact_id":      updated.ContactID,
		"status":          updated.Status,
		"priority":        updated.Priority,
	})
	out := map[string]any{"conversation": updated}
	if suppression.Attempted || suppression.Error != "" {
		out["suppression"] = suppression
	}
	return out, nil
}

func (a *App) toolGetConversation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	convoID := int64Arg(args, "conversation_id")
	if convoID == 0 {
		return nil, errors.New("conversation_id required")
	}
	convo, err := dbConversationGet(ctx.AppDB(), pid, convoID)
	if err != nil {
		return nil, err
	}
	if convo == nil || (cid != 0 && convo.ContactID != cid) {
		return nil, errors.New("conversation not found")
	}
	activities, err := dbConversationActivities(ctx.AppDB(), pid, convoID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"conversation": convo,
		"activities":   activities,
	}, nil
}

// ─── Conversation participants + Inbox filters ────────────────────

type conversationParticipant struct {
	Role      string
	Address   string
	ContactID int64
}

func dbConversationParticipantsAdd(db *sql.DB, pid string, conversationID int64, channel string, parts []conversationParticipant) error {
	if conversationID == 0 || len(parts) == 0 {
		return nil
	}
	stmt, err := db.Prepare(
		`INSERT OR IGNORE INTO conversation_participants
			(project_id, conversation_id, contact_id, role, channel, address, domain)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range parts {
		role := strings.ToLower(strings.TrimSpace(p.Role))
		if role != "from" && role != "to" && role != "cc" && role != "bcc" {
			continue
		}
		addr := canonicalParticipantAddress(channel, p.Address)
		if addr == "" {
			continue
		}
		var cid any
		if p.ContactID != 0 {
			cid = p.ContactID
		}
		domain := ""
		if channel == channelEmail {
			domain = domainOf(addr)
		}
		if _, err := stmt.Exec(pid, conversationID, cid, role, channel, addr, domain); err != nil {
			return err
		}
	}
	return nil
}

func canonicalParticipantAddress(channel, addr string) string {
	if channel == channelEmail {
		return canonicalAddress(channelEmail, addr)
	}
	if channel == channelSMS || channel == channelWhatsApp {
		return normaliseChannel("phone", addr)
	}
	return strings.ToLower(strings.TrimSpace(addr))
}

type inboxFilter struct {
	Field string
	Op    string
	Value any
}

func inboxFiltersFromArgs(args map[string]any) ([]inboxFilter, error) {
	var out []inboxFilter
	if raw, ok := args["filters"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return nil, errors.New("filters must be an array")
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("filters entries must be objects")
			}
			out = append(out, inboxFilter{
				Field: strings.ToLower(strings.TrimSpace(strFromAny(m["field"]))),
				Op:    strings.ToLower(strings.TrimSpace(strFromAny(m["op"]))),
				Value: m["value"],
			})
		}
	}
	for _, f := range []struct {
		arg   string
		field string
	}{
		{"channel", "channel"},
		{"from", "from"},
		{"to", "to"},
		{"cc", "cc"},
		{"bcc", "bcc"},
		{"tag", "tag"},
		{"priority", "priority"},
	} {
		if v := strings.TrimSpace(strArg(args, f.arg)); v != "" {
			out = append(out, inboxFilter{Field: f.field, Op: "is", Value: v})
		}
	}
	if id := int64Arg(args, "list_id"); id != 0 {
		out = append(out, inboxFilter{Field: "list", Op: "is", Value: id})
	}
	if id := int64Arg(args, "contact_id"); id != 0 {
		out = append(out, inboxFilter{Field: "contact", Op: "is", Value: id})
	}
	return out, nil
}

func buildInboxFilterWhere(filters []inboxFilter) ([]string, []any, error) {
	where := []string{}
	args := []any{}
	for _, f := range filters {
		if f.Field == "" {
			continue
		}
		op := f.Op
		if op == "" {
			op = "is"
		}
		clause, vals, err := buildInboxFilterClause(f.Field, op, f.Value)
		if err != nil {
			return nil, nil, err
		}
		if clause != "" {
			where = append(where, clause)
			args = append(args, vals...)
		}
	}
	return where, args, nil
}

func buildInboxFilterClause(field, op string, value any) (string, []any, error) {
	switch field {
	case "channel":
		return scalarInboxClause("cc.channel", op, value)
	case "priority":
		return scalarInboxClause("COALESCE(cc.priority,'normal')", op, value)
	case "contact":
		id := int64FromAny(value)
		if id != 0 {
			return "cc.contact_id = ?", []any{id}, nil
		}
		addr := strings.ToLower(strings.TrimSpace(strFromAny(value)))
		if addr == "" {
			return "", nil, errors.New("contact filter requires id or email")
		}
		return `(LOWER(c.primary_email) = ? OR EXISTS (
				SELECT 1 FROM contact_channels ch
				WHERE ch.project_id = cc.project_id AND ch.contact_id = cc.contact_id
				  AND ch.kind = 'email' AND LOWER(ch.value) = ?
			))`, []any{addr, addr}, nil
	case "list", "list_id":
		id := int64FromAny(value)
		if id == 0 {
			return "", nil, errors.New("list filter requires id")
		}
		return `EXISTS (
			SELECT 1 FROM contact_list_members lm
			WHERE lm.project_id = cc.project_id AND lm.contact_id = cc.contact_id AND lm.list_id = ?
		)`, []any{id}, nil
	case "tag":
		tag := strings.TrimSpace(strFromAny(value))
		if tag == "" {
			return "", nil, errors.New("tag filter requires value")
		}
		return `EXISTS (
			SELECT 1 FROM contact_tags t
			WHERE t.project_id = cc.project_id AND t.contact_id = cc.contact_id AND t.tag_name = ?
		)`, []any{tag}, nil
	case "from", "to", "cc", "bcc":
		return participantInboxClause(field, op, value)
	}
	return "", nil, fmt.Errorf("unsupported inbox filter field %q", field)
}

func scalarInboxClause(col, op string, value any) (string, []any, error) {
	v := strings.ToLower(strings.TrimSpace(strFromAny(value)))
	if v == "" {
		return "", nil, errors.New("filter value required")
	}
	switch op {
	case "is", "eq":
		return col + " = ?", []any{v}, nil
	case "is_not", "neq":
		return col + " <> ?", []any{v}, nil
	case "in":
		vals := stringValuesFromAny(value)
		if len(vals) == 0 {
			return "", nil, errors.New("in op requires values")
		}
		ph := strings.TrimRight(strings.Repeat("?,", len(vals)), ",")
		args := make([]any, 0, len(vals))
		for _, s := range vals {
			args = append(args, strings.ToLower(strings.TrimSpace(s)))
		}
		return col + " IN (" + ph + ")", args, nil
	}
	return "", nil, fmt.Errorf("unsupported op %q for %s", op, col)
}

func participantInboxClause(role, op string, value any) (string, []any, error) {
	if op == "in" {
		vals := stringValuesFromAny(value)
		if len(vals) == 0 {
			return "", nil, errors.New("in op requires values")
		}
		args := []any{role}
		for _, v := range vals {
			args = append(args, participantAddressCandidates(v)...)
		}
		ph := strings.TrimRight(strings.Repeat("?,", len(args)-1), ",")
		return `EXISTS (
			SELECT 1 FROM conversation_participants p
			WHERE p.project_id = cc.project_id AND p.conversation_id = cc.id
			  AND p.role = ? AND p.address IN (` + ph + `)
		)`, args, nil
	}
	raw := strings.TrimSpace(strFromAny(value))
	if raw == "" {
		return "", nil, errors.New("participant filter requires value")
	}
	switch op {
	case "is", "eq":
		vals := participantAddressCandidates(raw)
		ph := strings.TrimRight(strings.Repeat("?,", len(vals)), ",")
		args := append([]any{role}, vals...)
		return `EXISTS (
			SELECT 1 FROM conversation_participants p
			WHERE p.project_id = cc.project_id AND p.conversation_id = cc.id
			  AND p.role = ? AND p.address IN (` + ph + `)
		)`, args, nil
	case "is_not", "neq":
		vals := participantAddressCandidates(raw)
		ph := strings.TrimRight(strings.Repeat("?,", len(vals)), ",")
		args := append([]any{role}, vals...)
		return `NOT EXISTS (
			SELECT 1 FROM conversation_participants p
			WHERE p.project_id = cc.project_id AND p.conversation_id = cc.id
			  AND p.role = ? AND p.address IN (` + ph + `)
		)`, args, nil
	case "contains":
		return `EXISTS (
			SELECT 1 FROM conversation_participants p
			WHERE p.project_id = cc.project_id AND p.conversation_id = cc.id
			  AND p.role = ? AND p.address LIKE ?
		)`, []any{role, "%" + strings.ToLower(raw) + "%"}, nil
	case "domain":
		d := strings.TrimPrefix(strings.ToLower(raw), "@")
		return `EXISTS (
			SELECT 1 FROM conversation_participants p
			WHERE p.project_id = cc.project_id AND p.conversation_id = cc.id
			  AND p.role = ? AND p.domain = ?
		)`, []any{role, d}, nil
	}
	return "", nil, fmt.Errorf("unsupported participant op %q", op)
}

func participantAddressCandidates(raw string) []any {
	seen := map[string]bool{}
	out := []any{}
	for _, v := range []string{
		canonicalParticipantAddress(channelEmail, raw),
		canonicalParticipantAddress(channelSMS, raw),
		strings.ToLower(strings.TrimSpace(raw)),
	} {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func strFromAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(v)
	}
}

func stringValuesFromAny(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(strFromAny(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	default:
		return nil
	}
}

// ─── Inbox (cross-contact triage queue) ───────────────────────────

type inboxRow struct {
	ID             int64  `json:"id"`
	ContactID      int64  `json:"contact_id"`
	ContactName    string `json:"contact_name,omitempty"`
	ContactEmail   string `json:"contact_email,omitempty"`
	Channel        string `json:"channel"`
	Subject        string `json:"subject,omitempty"`
	Status         string `json:"status"`
	Priority       string `json:"priority"`
	LastActivityAt string `json:"last_activity_at"`
	Snippet        string `json:"snippet,omitempty"`
	Automated      bool   `json:"automated,omitempty"`
}

// dbInboxConversations returns conversations across all contacts for the
// triage queue. status="" defaults to 'open'; status="all" returns every
// status. Each row carries the contact summary, a last-message snippet,
// and whether the contact is tagged automated.
func dbInboxConversations(db *sql.DB, pid, status string, limit int, filters []inboxFilter) ([]*inboxRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "cc.project_id = ? AND c.deleted_at IS NULL"
	args := []any{pid}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "open":
		where += " AND COALESCE(cc.status,'open') = 'open'"
	case "all":
		// no status filter
	default:
		where += " AND COALESCE(cc.status,'open') = ?"
		args = append(args, strings.ToLower(strings.TrimSpace(status)))
	}
	filterWhere, filterArgs, err := buildInboxFilterWhere(filters)
	if err != nil {
		return nil, err
	}
	for _, clause := range filterWhere {
		where += " AND " + clause
	}
	args = append(args, filterArgs...)
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT cc.id, cc.contact_id, COALESCE(c.display_name,''), COALESCE(c.primary_email,''),
				cc.channel, COALESCE(cc.subject,''), COALESCE(cc.status,'open'),
				COALESCE(cc.priority,'normal'), cc.last_activity_at,
				COALESCE((SELECT a.body FROM contact_activities a
						  WHERE a.conversation_id = cc.id
						  ORDER BY a.occurred_at DESC, a.id DESC LIMIT 1), ''),
				EXISTS (SELECT 1 FROM contact_tags t
						WHERE t.contact_id = cc.contact_id AND t.tag_name = ?)
		 FROM contact_conversations cc
		 JOIN contacts c ON c.id = cc.contact_id
		 WHERE `+where+`
		 ORDER BY cc.last_activity_at DESC LIMIT ?`,
		append([]any{tagAutomated}, args...)...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*inboxRow{}
	for rows.Next() {
		r := &inboxRow{}
		var autom int
		if err := rows.Scan(&r.ID, &r.ContactID, &r.ContactName, &r.ContactEmail,
			&r.Channel, &r.Subject, &r.Status, &r.Priority, &r.LastActivityAt,
			&r.Snippet, &autom); err != nil {
			return nil, err
		}
		r.Automated = autom != 0
		r.Snippet = truncate(r.Snippet, 160)
		out = append(out, r)
	}
	return out, rows.Err()
}

// toolInbox — cross-contact triage queue. Args: status? (open|all|…),
// limit?. Defaults to open conversations, newest activity first.
func (a *App) toolInbox(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status")
	limit := intArg(args, "limit", 50)
	filters, err := inboxFiltersFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := dbInboxConversations(ctx.AppDB(), pid, status, limit, filters)
	if err != nil {
		return nil, err
	}
	return map[string]any{"inbox": items, "count": len(items)}, nil
}

// ─── Routing rules ────────────────────────────────────────────────

func (a *App) toolRoutingRulesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r := &routingRule{
		Name:           strArg(args, "name"),
		MatchRecipient: strArg(args, "match_recipient"),
		MatchSender:    strArg(args, "match_sender"),
		AddTag:         strArg(args, "add_tag"),
		Priority:       intArg(args, "priority", 0),
		Enabled:        true,
	}
	if v, ok := args["enabled"].(bool); ok {
		r.Enabled = v
	}
	if lid := int64Arg(args, "add_list_id"); lid != 0 {
		r.AddListID = &lid
	}
	id, err := dbCreateRoutingRule(ctx.AppDB(), pid, r)
	if err != nil {
		return nil, err
	}
	r.ID = id
	return map[string]any{"rule": r}, nil
}

func (a *App) toolRoutingRulesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rules, err := dbListRoutingRules(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rules": rules, "count": len(rules)}, nil
}

func (a *App) toolRoutingRulesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbDeleteRoutingRule(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id}, nil
}

// ─── Inbound webhook ──────────────────────────────────────────────

// inboundPayload mirrors what messaging.dispatchInbound POSTs to us.
// Field names match messaging/main.go:2466-2483.
type inboundPayload struct {
	MessageID        int64          `json:"message_id"`
	Channel          string         `json:"channel"`
	From             string         `json:"from"`
	To               []string       `json:"to"`
	CC               []string       `json:"cc"`
	Subject          string         `json:"subject"`
	BodyText         string         `json:"body_text"`
	BodyHTML         string         `json:"body_html"`
	MessageIDHeader  string         `json:"message_id_header"`
	InReplyTo        string         `json:"in_reply_to"`
	References       []string       `json:"references"`
	Headers          map[string]any `json:"headers"`
	ReceivedAt       string         `json:"received_at"`
	MatchedRecipient string         `json:"matched_recipient"`
	MatchedPattern   string         `json:"matched_pattern"`
	ToSubaddress     string         `json:"to_subaddress"`
}

func (a *App) handleInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body inboundPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "json: "+err.Error())
		return
	}
	if body.Channel == "" || body.From == "" {
		httpErr(w, http.StatusBadRequest, "channel and from required")
		return
	}
	out, err := ingestInbound(globalCtx, pid, body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) toolMessagingInboundReceive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	body := inboundPayload{
		MessageID:        int64Arg(args, "message_id"),
		Channel:          strArg(args, "channel"),
		From:             strArg(args, "from"),
		To:               stringSliceArg(args, "to"),
		CC:               stringSliceArg(args, "cc"),
		Subject:          strArg(args, "subject"),
		BodyText:         strArg(args, "body_text"),
		BodyHTML:         strArg(args, "body_html"),
		MessageIDHeader:  strArg(args, "message_id_header"),
		InReplyTo:        strArg(args, "in_reply_to"),
		References:       stringSliceArg(args, "references"),
		ReceivedAt:       strArg(args, "received_at"),
		MatchedRecipient: strArg(args, "matched_recipient"),
		MatchedPattern:   strArg(args, "matched_pattern"),
		ToSubaddress:     strArg(args, "to_subaddress"),
	}
	if headers, ok := args["headers"].(map[string]any); ok {
		body.Headers = headers
	} else {
		body.Headers = map[string]any{}
	}
	if body.Channel == "" || body.From == "" {
		return nil, errors.New("channel and from required")
	}
	return ingestInbound(ctx, pid, body)
}

func stringSliceArg(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []string:
		return append([]string{}, v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s := strings.TrimSpace(anyString(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	default:
		return nil
	}
}

func ingestInbound(ctx *sdk.AppCtx, pid string, body inboundPayload) (map[string]any, error) {
	if ctx == nil {
		return nil, errors.New("crm context not initialized")
	}
	if body.Channel == "" || body.From == "" {
		return nil, errors.New("channel and from required")
	}
	body.From = canonicalAddress(body.Channel, body.From)

	db := ctx.AppDB()

	contact, fuzzyCandidates, err := matchInboundContact(db, pid, body.Channel, body.From)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	stubCreated := false
	if contact == nil {
		defaults := map[string]any{
			"display_name": parseFromName(body.From, body.Headers),
			"source":       "messaging:inbound",
		}
		contact, _, err = dbUpsertByChannel(db, pid, contactChannelKindFor(body.Channel), body.From, defaults, "messaging:inbound")
		if err != nil {
			return nil, fmt.Errorf("upsert: %w", err)
		}
		stubCreated = true
		if len(fuzzyCandidates) > 0 {
			// Surface possible duplicates as a system activity so the
			// panel can render a "merge?" banner without a new column.
			_, _ = logMessageActivity(db, logMessageActivityInput{
				ProjectID: pid,
				ContactID: contact.ID,
				Kind:      ActivityKindSystem,
				Body:      "stub contact created from inbound; possible duplicates flagged",
				Source:    "crm",
				SourceDetail: map[string]any{
					"possible_match_ids": fuzzyCandidates,
				},
			})
		}
	}

	// Classify the sender. Machine / no-reply senders still get the
	// contact + activity (the message may matter) but are tagged
	// `automated` so bulk sends / segments can exclude them. Idempotent —
	// re-tagging on repeat inbound is a no-op.
	automated, autoReason := isAutomatedSender(body.Channel, body.From, body.Headers)
	if automated {
		if err := dbAddTag(db, pid, contact.ID, tagAutomated); err != nil {
			ctx.Logger().Warn("auto-tag automated sender", "contact_id", contact.ID, "err", err)
		}
	}

	// Routing rules: match on recipient (which of our addresses it hit)
	// and/or sender, then apply add-to-list / add-tag actions. Generalizes
	// the legacy list.inbound_route_pattern coupling below.
	routeRecipients := append([]string{body.MatchedRecipient}, body.To...)
	routed, err := applyRoutingRules(db, pid, contact.ID, routeRecipients, body.From)
	if err != nil {
		ctx.Logger().Warn("routing rules", "contact_id", contact.ID, "err", err)
	}
	for _, lid := range routed.Lists {
		emitCRMEvent(ctx, pid, "list.member.added", map[string]any{"list_id": lid, "contact_id": contact.ID})
	}

	convoID, err := resolveInboundConversation(db, pid, contact.ID, body)
	if err != nil {
		return nil, fmt.Errorf("convo: %w", err)
	}

	reopened := false
	contactSpam, err := dbContactIsSpam(db, pid, contact.ID)
	if err != nil {
		return nil, fmt.Errorf("spam contact check: %w", err)
	}
	if contactSpam {
		if _, err := dbConversationSetStatus(db, pid, convoID, "spam", ""); err != nil {
			return nil, fmt.Errorf("mark spam conversation: %w", err)
		}
	} else {
		// Auto-reopen: a reply on a pending/closed thread puts the ball back
		// in our court. No-op when the conversation is already open (or was
		// just created above). Spam conversations are intentionally excluded.
		reopened, err = dbConversationReopenIfClosed(db, pid, convoID)
		if err != nil {
			return nil, fmt.Errorf("reopen: %w", err)
		}
	}

	occurred := body.ReceivedAt
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339)
	}
	activityBody := body.BodyText
	if body.Channel == channelEmail && body.Subject != "" {
		activityBody = body.Subject + "\n\n" + body.BodyText
	}

	act, err := logMessageActivity(db, logMessageActivityInput{
		ProjectID:  pid,
		ContactID:  contact.ID,
		Kind:       receivedKindForChannel(body.Channel),
		Body:       truncate(activityBody, 4000),
		OccurredAt: occurred,
		Source:     "messaging",
		SourceDetail: map[string]any{
			"messaging_id":      body.MessageID,
			"message_id_header": body.MessageIDHeader,
			"in_reply_to":       body.InReplyTo,
			"matched_pattern":   body.MatchedPattern,
			"to":                body.To,
			"sender_automated":  automated,
			"sender_class":      autoReason,
		},
		ConversationID:  convoID,
		MessageIDHeader: body.MessageIDHeader,
		MessagingID:     body.MessageID,
	})
	if errors.Is(err, errDuplicateMessagingID) {
		// Idempotent re-delivery — already logged. Return ok so messaging
		// stops retrying.
		return map[string]any{"ok": true, "deduped": true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	parts := []conversationParticipant{{Role: "from", Address: body.From, ContactID: contact.ID}}
	seenTo := map[string]bool{}
	for _, to := range body.To {
		key := canonicalParticipantAddress(body.Channel, to)
		if key == "" || seenTo[key] {
			continue
		}
		seenTo[key] = true
		parts = append(parts, conversationParticipant{Role: "to", Address: to})
	}
	if mr := canonicalParticipantAddress(body.Channel, body.MatchedRecipient); mr != "" && !seenTo[mr] {
		parts = append(parts, conversationParticipant{Role: "to", Address: body.MatchedRecipient})
	}
	for _, cc := range body.CC {
		parts = append(parts, conversationParticipant{Role: "cc", Address: cc})
	}
	_ = dbConversationParticipantsAdd(db, pid, convoID, body.Channel, parts)

	// List auto-attach. If messaging's matched_pattern matches a list's
	// inbound_route_pattern, add this contact to that list. Idempotent
	// (INSERT OR IGNORE), so repeat inbound from the same address is a
	// no-op for an already-attached contact.
	var listID int64
	if body.MatchedPattern != "" {
		if l, _ := dbListByInboundPattern(db, pid, body.MatchedPattern); l != nil {
			listID = l.ID
			if err := dbListAddContact(db, pid, l.ID, contact.ID, "messaging:inbound"); err != nil {
				ctx.Logger().Warn("inbound list auto-attach failed", "list_id", l.ID, "contact_id", contact.ID, "err", err)
			} else {
				emitCRMEvent(ctx, pid, "list.member.added", map[string]any{"list_id": l.ID, "contact_id": contact.ID})
			}
		}
	}

	if stubCreated {
		emitCRMEvent(ctx, pid, "contact.added", map[string]any{
			"id": contact.ID, "display_name": contact.DisplayName,
		})
	}
	if reopened {
		emitCRMEvent(ctx, pid, "conversation.status.changed", map[string]any{
			"conversation_id": convoID,
			"contact_id":      contact.ID,
			"status":          "open",
			"reason":          "inbound_reply",
		})
	} else if contactSpam {
		emitCRMEvent(ctx, pid, "conversation.status.changed", map[string]any{
			"conversation_id": convoID,
			"contact_id":      contact.ID,
			"status":          "spam",
			"reason":          "spam_contact_inbound",
		})
	}
	emitCRMEvent(ctx, pid, "contact.activity.added", map[string]any{
		"contact_id":  contact.ID,
		"activity_id": act.ID,
		"kind":        act.Kind,
	})

	out := map[string]any{
		"ok":              true,
		"contact_id":      contact.ID,
		"stub_created":    stubCreated,
		"activity_id":     act.ID,
		"conversation_id": convoID,
	}
	if listID != 0 {
		out["list_id"] = listID
	}
	return out, nil
}

func resolveInboundConversation(db *sql.DB, pid string, contactID int64, p inboundPayload) (int64, error) {
	if p.Channel == channelSMS || p.Channel == channelWhatsApp {
		existing, err := dbConversationForChannel(db, pid, contactID, p.Channel)
		if err != nil {
			return 0, err
		}
		if existing != nil {
			return existing.ID, nil
		}
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		now := p.ReceivedAt
		if now == "" {
			now = time.Now().UTC().Format(time.RFC3339)
		}
		id, err := dbConversationCreate(tx, pid, contactID, p.Channel, "", "", now)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return id, nil
	}

	// email — chain match by In-Reply-To then References.
	if p.InReplyTo != "" {
		if id, err := dbConversationByActivityMsgID(db, pid, contactID, p.InReplyTo); err == nil && id != 0 {
			return id, nil
		}
		if c, err := dbConversationByRootMsgID(db, pid, p.InReplyTo); err == nil && c != nil {
			return c.ID, nil
		}
	}
	for _, ref := range p.References {
		if c, err := dbConversationByRootMsgID(db, pid, ref); err == nil && c != nil {
			return c.ID, nil
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	now := p.ReceivedAt
	if now == "" {
		now = time.Now().UTC().Format(time.RFC3339)
	}
	id, err := dbConversationCreate(tx, pid, contactID, channelEmail, p.Subject, p.MessageIDHeader, now)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// matchInboundContact returns the contact matched on exact address
// (primary_* or contact_channels), or nil + a list of fuzzy-candidate
// contact ids the operator should review for merge. Fuzzy matching is
// email-only for v0.1: domain match, then domain+name overlap when the
// inbound carries a display name.
func matchInboundContact(db *sql.DB, pid, channel, from string) (*Contact, []int64, error) {
	if c, _ := dbGetByPrimary(db, pid, contactChannelKindFor(channel), from); c != nil {
		return c, nil, nil
	}
	row := db.QueryRow(
		`SELECT contact_id FROM contact_channels
		 WHERE project_id = ? AND kind = ? AND value = ? LIMIT 1`,
		pid, contactChannelKindFor(channel), from,
	)
	var cid int64
	if err := row.Scan(&cid); err == nil && cid != 0 {
		c, err := dbGetByID(db, pid, cid)
		return c, nil, err
	}

	if channel != channelEmail {
		return nil, nil, nil
	}
	domain := domainOf(from)
	if domain == "" {
		return nil, nil, nil
	}
	rows, err := db.Query(
		`SELECT id FROM contacts
		 WHERE project_id = ? AND deleted_at IS NULL
		   AND (status IS NULL OR status = 'active')
		   AND (
				LOWER(primary_email) LIKE ?
				OR LOWER(COALESCE(company,'')) = ?
		   )
		 LIMIT 20`,
		pid, "%@"+domain, domain,
	)
	if err != nil {
		return nil, nil, nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return nil, out, nil
}

// ─── Channel + format helpers ─────────────────────────────────────

func sentKindForChannel(channel string) string {
	switch channel {
	case channelEmail:
		return ActivityKindEmailSent
	case channelSMS:
		return ActivityKindSMSSent
	case channelWhatsApp:
		return ActivityKindWhatsAppSent
	}
	return ""
}
func receivedKindForChannel(channel string) string {
	switch channel {
	case channelEmail:
		return ActivityKindEmailReceived
	case channelSMS:
		return ActivityKindSMSReceived
	case channelWhatsApp:
		return ActivityKindWhatsAppReceived
	}
	return ""
}
func failedKindForChannel(channel string) string {
	switch channel {
	case channelEmail:
		return ActivityKindEmailSendFailed
	case channelSMS:
		return ActivityKindSMSSendFailed
	case channelWhatsApp:
		return ActivityKindWhatsAppSendFailed
	}
	return ""
}
func testSentKindForChannel(channel string) string {
	switch channel {
	case channelEmail:
		return ActivityKindEmailTestSent
	case channelSMS:
		return ActivityKindSMSTestSent
	case channelWhatsApp:
		return ActivityKindWhatsAppTestSent
	}
	return ""
}

func contactChannelKindFor(channel string) string {
	switch channel {
	case channelEmail:
		return "email"
	case channelSMS, channelWhatsApp:
		return "phone"
	}
	return ""
}

func defaultSenderForChannel(ctx *sdk.AppCtx, channel string) string {
	if ctx == nil {
		return ""
	}
	cfg := ctx.Config()
	switch channel {
	case channelEmail:
		return strings.TrimSpace(cfg.Get("default_sender_email"))
	case channelSMS, channelWhatsApp:
		return strings.TrimSpace(cfg.Get("default_sender_phone"))
	}
	return ""
}

func canonicalAddress(channel, addr string) string {
	addr = strings.TrimSpace(addr)
	if channel == channelEmail {
		if a, err := mail.ParseAddress(addr); err == nil {
			return strings.ToLower(a.Address)
		}
		return strings.ToLower(addr)
	}
	return addr
}

func domainOf(email string) string {
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[i+1:]))
}

func parseFromName(addr string, headers map[string]any) string {
	if hf, _ := headers["From"].(string); hf != "" {
		if a, err := mail.ParseAddress(hf); err == nil && a.Name != "" {
			return a.Name
		}
	}
	if a, err := mail.ParseAddress(addr); err == nil && a.Name != "" {
		return a.Name
	}
	return addr
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ─── HTTP wrappers (panel surface) ────────────────────────────────

// pathSegmentAfter returns the n-th segment of r.URL.Path under
// /contacts/. e.g. /contacts/42/messages → segmentAt(1)="messages".
func contactsPathParts(r *http.Request) []string {
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}

func (a *App) handleHTTPSendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	parts := contactsPathParts(r)
	if len(parts) < 1 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	args := mustReadJSONArgs(r)
	args["id"] = parts[0]
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolSendMessage(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	parts := contactsPathParts(r)
	if len(parts) < 1 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	args := mustReadJSONArgs(r)
	args["id"] = parts[0]
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolReply(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPMessagingSenders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{
		"_project_id":   pid,
		"verified_only": true,
	}
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		args["channel"] = channel
	}
	var out map[string]any
	if err := callMessagingTool(globalCtx, "senders_list", args, &out); err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPMessagingTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{
		"_project_id": pid,
		"limit":       200,
	}
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		args["channel"] = channel
	}
	var out map[string]any
	if err := callMessagingTool(globalCtx, "template_list", args, &out); err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPMessagingWhatsAppSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from := cleanMessagingAddress(r.URL.Query().Get("from"))
	to := cleanMessagingAddress(r.URL.Query().Get("to"))
	if from == "" || to == "" {
		httpErr(w, http.StatusBadRequest, "from and to required")
		return
	}
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	var out struct {
		Messages []struct {
			From             string   `json:"from"`
			To               []string `json:"to"`
			MatchedRecipient string   `json:"matched_recipient"`
			ReceivedAt       string   `json:"received_at"`
			CreatedAt        string   `json:"created_at"`
		} `json:"messages"`
	}
	err = callMessagingTool(globalCtx, "message_list", map[string]any{
		"_project_id": pid,
		"direction":   "in",
		"channel":     "whatsapp",
		"address":     to,
		"since":       since,
		"limit":       50,
	}, &out)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	active := false
	var lastInbound string
	for _, m := range out.Messages {
		if cleanMessagingAddress(m.From) != to {
			continue
		}
		matched := cleanMessagingAddress(m.MatchedRecipient) == from
		for _, recipient := range m.To {
			if cleanMessagingAddress(recipient) == from {
				matched = true
				break
			}
		}
		if matched {
			active = true
			if m.ReceivedAt != "" {
				lastInbound = m.ReceivedAt
			} else {
				lastInbound = m.CreatedAt
			}
			break
		}
	}
	httpJSON(w, map[string]any{
		"active":       active,
		"from":         from,
		"to":           to,
		"since":        since,
		"last_inbound": lastInbound,
	})
}

func cleanMessagingAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ":"); i > 0 {
		prefix := strings.ToLower(strings.TrimSpace(s[:i]))
		if prefix == "mailto" || prefix == "sms" || prefix == "whatsapp" || prefix == "tel" {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	if strings.Contains(s, "@") {
		return strings.ToLower(s)
	}
	return s
}

func (a *App) handleHTTPListConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	parts := contactsPathParts(r)
	if len(parts) < 1 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	args := map[string]any{"id": parts[0]}
	if ch := r.URL.Query().Get("channel"); ch != "" {
		args["channel"] = ch
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		args["limit"] = l
	}
	for _, k := range []string{"channel", "from", "to", "cc", "bcc", "tag", "priority"} {
		if v := r.URL.Query().Get(k); v != "" {
			args[k] = v
		}
	}
	if v := r.URL.Query().Get("list_id"); v != "" {
		args["list_id"] = v
	}
	if v := r.URL.Query().Get("contact_id"); v != "" {
		args["contact_id"] = v
	}
	if raw := r.URL.Query().Get("filters"); raw != "" {
		var filters []any
		if err := json.Unmarshal([]byte(raw), &filters); err != nil {
			httpErr(w, http.StatusBadRequest, "filters must be a JSON array: "+err.Error())
			return
		}
		args["filters"] = filters
	}
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolListConversations(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPGetConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	parts := contactsPathParts(r)
	// /contacts/<id>/conversations/<cid>
	if len(parts) < 3 || parts[0] == "" || parts[2] == "" {
		httpErr(w, http.StatusBadRequest, "id and conversation_id required")
		return
	}
	args := map[string]any{"id": parts[0], "conversation_id": parts[2]}
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolGetConversation(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

// handleHTTPSetConversationStatus handles
// POST /contacts/<id>/conversations/<cid>/status  body: {status?, priority?}
func (a *App) handleHTTPSetConversationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	parts := contactsPathParts(r)
	// /contacts/<id>/conversations/<cid>/status
	if len(parts) < 4 || parts[0] == "" || parts[2] == "" {
		httpErr(w, http.StatusBadRequest, "id and conversation_id required")
		return
	}
	args := mustReadJSONArgs(r)
	args["id"] = parts[0]
	args["conversation_id"] = parts[2]
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolSetConversationStatus(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

// handleHTTPInbox — GET /inbox?status=&limit= → cross-contact triage queue.
func (a *App) handleHTTPInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	args := map[string]any{}
	if s := r.URL.Query().Get("status"); s != "" {
		args["status"] = s
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		args["limit"] = l
	}
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolInbox(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

// handleHTTPRoutingRules — GET /routing-rules (list) / POST (create).
func (a *App) handleHTTPRoutingRules(w http.ResponseWriter, r *http.Request) {
	pid, _ := resolveProjectFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{}
		if pid != "" {
			args["_project_id"] = pid
		}
		out, err := a.toolRoutingRulesList(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		args := mustReadJSONArgs(r)
		if pid != "" {
			args["_project_id"] = pid
		}
		out, err := a.toolRoutingRulesCreate(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

// handleHTTPRoutingRuleItem — DELETE /routing-rules/<id>.
func (a *App) handleHTTPRoutingRuleItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpErr(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/routing-rules/")
	if id == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	args := map[string]any{"id": id}
	if pid, _ := resolveProjectFromRequest(r); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolRoutingRulesDelete(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func mustReadJSONArgs(r *http.Request) map[string]any {
	out := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}
