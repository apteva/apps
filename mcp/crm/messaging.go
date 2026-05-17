// CRM ↔ messaging coupling.
//
// CRM is the agent's single interface for talking to people. This
// file wires it to the optional messaging app: outbound via
// contacts_send_message / contacts_reply, inbound via POST /inbound,
// and conversation threading on top.
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
	ID             int64  `json:"id"`
	ContactID      int64  `json:"contact_id"`
	Channel        string `json:"channel"`
	Subject        string `json:"subject,omitempty"`
	RootMessageID  string `json:"root_message_id,omitempty"`
	StartedAt      string `json:"started_at"`
	LastActivityAt string `json:"last_activity_at"`
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
		`SELECT id, contact_id, channel,
				COALESCE(subject,''), COALESCE(root_message_id,''),
				started_at, last_activity_at
		 FROM contact_conversations
		 WHERE project_id = ? AND contact_id = ? AND channel = ?
		 ORDER BY id ASC LIMIT 1`,
		pid, contactID, channel,
	)
	c := &Conversation{}
	if err := row.Scan(&c.ID, &c.ContactID, &c.Channel, &c.Subject, &c.RootMessageID, &c.StartedAt, &c.LastActivityAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

func dbConversationByRootMsgID(db *sql.DB, pid, rootMsgID string) (*Conversation, error) {
	if rootMsgID == "" {
		return nil, nil
	}
	row := db.QueryRow(
		`SELECT id, contact_id, channel,
				COALESCE(subject,''), COALESCE(root_message_id,''),
				started_at, last_activity_at
		 FROM contact_conversations
		 WHERE project_id = ? AND root_message_id = ? LIMIT 1`,
		pid, rootMsgID,
	)
	c := &Conversation{}
	if err := row.Scan(&c.ID, &c.ContactID, &c.Channel, &c.Subject, &c.RootMessageID, &c.StartedAt, &c.LastActivityAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
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

func dbConversationsList(db *sql.DB, pid string, contactID int64, channel string, limit int) ([]*Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "project_id = ? AND contact_id = ?"
	args := []any{pid, contactID}
	if channel != "" {
		where += " AND channel = ?"
		args = append(args, channel)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, contact_id, channel,
				COALESCE(subject,''), COALESCE(root_message_id,''),
				started_at, last_activity_at
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
		c := &Conversation{}
		if err := rows.Scan(&c.ID, &c.ContactID, &c.Channel, &c.Subject, &c.RootMessageID, &c.StartedAt, &c.LastActivityAt); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func dbConversationGet(db *sql.DB, pid string, id int64) (*Conversation, error) {
	row := db.QueryRow(
		`SELECT id, contact_id, channel,
				COALESCE(subject,''), COALESCE(root_message_id,''),
				started_at, last_activity_at
		 FROM contact_conversations
		 WHERE project_id = ? AND id = ?`,
		pid, id,
	)
	c := &Conversation{}
	if err := row.Scan(&c.ID, &c.ContactID, &c.Channel, &c.Subject, &c.RootMessageID, &c.StartedAt, &c.LastActivityAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
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
	cid := int64Arg(args, "id")
	if cid == 0 {
		cid = int64Arg(args, "contact_id")
	}
	if cid == 0 {
		return nil, errors.New("id (contact id) required")
	}
	body, _ := args["body"].(string)
	if body == "" {
		return nil, errors.New("body required")
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
	if idem := strArg(args, "idempotency_key"); idem != "" {
		sendArgs["idempotency_key"] = idem
	}
	if vars, ok := args["template_vars"].(map[string]any); ok {
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
			"to":                  addr.Address,
			"test":                isTest,
		},
		ConversationID:  convoIDForLog,
		MessageIDHeader: providerMsgID,
	})
	if err != nil {
		return nil, err
	}
	ctx.Emit("contact.activity.added", map[string]any{
		"contact_id": cid, "kind": kind,
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
	if body == "" {
		return nil, errors.New("body required")
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
	limit := intArg(args, "limit", 50)
	out, err := dbConversationsList(ctx.AppDB(), pid, cid, channel, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversations": out, "count": len(out)}, nil
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
	body.From = canonicalAddress(body.Channel, body.From)

	db := globalCtx.AppDB()

	contact, fuzzyCandidates, err := matchInboundContact(db, pid, body.Channel, body.From)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "match: "+err.Error())
		return
	}
	stubCreated := false
	if contact == nil {
		defaults := map[string]any{
			"display_name": parseFromName(body.From, body.Headers),
			"source":       "messaging:inbound",
		}
		contact, _, err = dbUpsertByChannel(db, pid, contactChannelKindFor(body.Channel), body.From, defaults, "messaging:inbound")
		if err != nil {
			httpErr(w, http.StatusInternalServerError, "upsert: "+err.Error())
			return
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

	convoID, err := resolveInboundConversation(db, pid, contact.ID, body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "convo: "+err.Error())
		return
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
		},
		ConversationID:  convoID,
		MessageIDHeader: body.MessageIDHeader,
		MessagingID:     body.MessageID,
	})
	if errors.Is(err, errDuplicateMessagingID) {
		// Idempotent re-delivery — already logged. Return ok so messaging
		// stops retrying.
		httpJSON(w, map[string]any{"ok": true, "deduped": true})
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "log: "+err.Error())
		return
	}

	// List auto-attach. If messaging's matched_pattern matches a list's
	// inbound_route_pattern, add this contact to that list. Idempotent
	// (INSERT OR IGNORE), so repeat inbound from the same address is a
	// no-op for an already-attached contact.
	var listID int64
	if body.MatchedPattern != "" {
		if l, _ := dbListByInboundPattern(db, pid, body.MatchedPattern); l != nil {
			listID = l.ID
			if err := dbListAddContact(db, pid, l.ID, contact.ID, "messaging:inbound"); err != nil {
				globalCtx.Logger().Warn("inbound list auto-attach failed", "list_id", l.ID, "contact_id", contact.ID, "err", err)
			} else {
				globalCtx.Emit("list.member.added", map[string]any{"list_id": l.ID, "contact_id": contact.ID})
			}
		}
	}

	if stubCreated {
		globalCtx.Emit("contact.added", map[string]any{
			"id": contact.ID, "display_name": contact.DisplayName,
		})
	}
	globalCtx.Emit("contact.activity.added", map[string]any{
		"contact_id": contact.ID, "kind": act.Kind,
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
	httpJSON(w, out)
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

func mustReadJSONArgs(r *http.Request) map[string]any {
	out := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}
