package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

type draftContent struct {
	Channel      string           `json:"channel"`
	To           string           `json:"to"`
	From         string           `json:"from"`
	Subject      string           `json:"subject"`
	Body         string           `json:"body"`
	BodyHTML     string           `json:"body_html,omitempty"`
	TemplateID   int64            `json:"template_id,omitempty"`
	ContentSID   string           `json:"content_sid,omitempty"`
	TemplateVars map[string]any   `json:"template_vars,omitempty"`
	Attachments  []map[string]any `json:"attachments,omitempty"`
}

type conversationDraft struct {
	ID                 int64           `json:"id"`
	ContactID          int64           `json:"contact_id"`
	ConversationID     int64           `json:"conversation_id"`
	ReplyToActivityID  int64           `json:"reply_to_activity_id"`
	Content            draftContent    `json:"content"`
	Status             string          `json:"status"`
	Revision           int64           `json:"revision"`
	CreatedBy          string          `json:"created_by"`
	UpdatedBy          string          `json:"updated_by"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	LastError          string          `json:"last_error,omitempty"`
	Result             json.RawMessage `json:"sent_result,omitempty"`
	MessagingInstallID int64           `json:"-"`
	AttemptKey         string          `json:"-"`
	LeaseUntil         string          `json:"-"`
	Dispatched         bool            `json:"-"`
}

var errDraftConflict = errors.New("draft conflict: reload the latest draft; it changed or is not editable")
var errDraftNotFound = errors.New("draft not found in this project")

const draftColumns = `id,contact_id,conversation_id,reply_to_activity_id,content,status,revision,created_by,updated_by,created_at,updated_at,last_error,sent_result,messaging_install_id,attempt_key,lease_until,dispatched`

func scanDraft(row interface{ Scan(...any) error }) (*conversationDraft, error) {
	d := &conversationDraft{}
	var content string
	var result sql.NullString
	err := row.Scan(&d.ID, &d.ContactID, &d.ConversationID, &d.ReplyToActivityID, &content, &d.Status, &d.Revision, &d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt, &d.LastError, &result, &d.MessagingInstallID, &d.AttemptKey, &d.LeaseUntil, &d.Dispatched)
	if err == sql.ErrNoRows {
		return nil, errDraftNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(content), &d.Content); err != nil {
		return nil, err
	}
	if result.Valid {
		d.Result = json.RawMessage(result.String)
	}
	return d, nil
}

func getDraft(db *sql.DB, pid string, id int64) (*conversationDraft, error) {
	return scanDraft(db.QueryRow(`SELECT `+draftColumns+` FROM conversation_drafts WHERE project_id=? AND id=?`, pid, id))
}
func draftAuthor(args map[string]any) string {
	s := strings.TrimSpace(strArg(args, "source"))
	if s == "" {
		s = "agent"
	}
	return truncate(s, 200)
}
func draftNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func draftChanged(ctx *sdk.AppCtx, pid string, d *conversationDraft) {
	emitCRMEvent(ctx, pid, "conversation.draft.changed", map[string]any{"draft_id": d.ID, "conversation_id": d.ConversationID, "contact_id": d.ContactID, "revision": d.Revision, "status": d.Status})
}

func validateDraftContent(c draftContent) error {
	if c.Channel != "email" && !phoneTransport(c.Channel) {
		return errors.New("draft channel must be email, sms or whatsapp")
	}
	if c.TemplateID < 0 {
		return errors.New("template_id cannot be negative")
	}
	if c.From != "" {
		if c.Channel == channelEmail && inboundEmailRecipient(c.From) == "" {
			return errors.New("draft from must be a valid email address")
		}
		if phoneTransport(c.Channel) && !looksLikeE164(canonicalParticipantAddress(c.Channel, c.From)) {
			return errors.New("draft from must be E.164")
		}
	}
	args := map[string]any{"attachments": c.Attachments}
	if err := validateOutboundAttachments(args); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if int64(len(raw)) > maxMessageJSONBodyBytes {
		return errors.New("draft content is too large")
	}
	return nil
}

// Updates replace only explicitly supplied content fields. Address identity
// remains pinned; a channel change is allowed only for the same phone number.
func patchDraftContent(c draftContent, args map[string]any) (draftContent, error) {
	for key, value := range args {
		switch key {
		case "channel", "from", "subject", "body", "body_html", "content_sid":
			s, ok := value.(string)
			if !ok {
				return c, fmt.Errorf("%s must be a string", key)
			}
			switch key {
			case "channel":
				c.Channel = strings.ToLower(strings.TrimSpace(s))
			case "from":
				c.From = s
			case "subject":
				c.Subject = s
			case "body":
				c.Body = s
			case "body_html":
				c.BodyHTML = s
			case "content_sid":
				c.ContentSID = s
			}
		case "template_id":
			n, err := strconv.ParseInt(anyString(value), 10, 64)
			if err != nil {
				return c, errors.New("template_id must be an integer")
			}
			c.TemplateID = n
		case "template_vars":
			raw, err := json.Marshal(value)
			if err != nil {
				return c, err
			}
			if string(raw) == "null" {
				return c, errors.New("template_vars must be an object")
			}
			if err = json.Unmarshal(raw, &c.TemplateVars); err != nil {
				return c, errors.New("template_vars must be an object")
			}
		case "attachments":
			raw, err := json.Marshal(value)
			if err != nil {
				return c, err
			}
			if string(raw) == "null" {
				return c, errors.New("attachments must be an array")
			}
			if err = json.Unmarshal(raw, &c.Attachments); err != nil {
				return c, errors.New("attachments must be an array")
			}
		case "to":
			if canonicalParticipantAddress(c.Channel, anyString(value)) != c.To {
				return c, errors.New("draft recipient is pinned to the original reply; create a new draft to change recipient")
			}
		case "contact_id", "conversation_id", "reply_to_activity_id", "id", "expected_revision", "source", "client_key", "_project_id":
			// Identity/CAS fields are handled separately, never copied into content.
		default:
			return c, fmt.Errorf("unsupported draft field %q", key)
		}
	}
	c.From = canonicalParticipantAddress(c.Channel, c.From)
	return c, validateDraftContent(c)
}

func (a *App) toolDraftCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	convo, err := dbConversationGet(ctx.AppDB(), pid, int64Arg(args, "conversation_id"))
	if err != nil {
		return nil, err
	}
	if convo == nil {
		return nil, errors.New("conversation not found in this project")
	}
	if cid := int64Arg(args, "contact_id"); cid > 0 && cid != convo.ContactID {
		return nil, errors.New("conversation does not belong to contact")
	}
	channel := strings.ToLower(strings.TrimSpace(strArg(args, "channel")))
	if channel == "" {
		channel = convo.Channel
	}
	if err = validateReplyTransport(convo.Channel, channel); err != nil {
		return nil, err
	}
	anchor := int64Arg(args, "reply_to_activity_id")
	if anchor == 0 {
		err = ctx.AppDB().QueryRow(`SELECT id FROM contact_activities WHERE project_id=? AND contact_id=? AND conversation_id=? AND kind IN ('email_received','sms_received','whatsapp_received') ORDER BY CASE WHEN kind=? THEN 0 ELSE 1 END,julianday(occurred_at) DESC,id DESC LIMIT 1`, pid, convo.ContactID, convo.ID, receivedKindForChannel(channel)).Scan(&anchor)
		if err == sql.ErrNoRows {
			return nil, errors.New("reply draft requires an inbound message in this conversation")
		}
		if err != nil {
			return nil, err
		}
	}
	to, from, err := replyRoute(ctx.AppDB(), pid, convo.ContactID, convo.ID, channel, anchor)
	if err != nil {
		return nil, err
	}
	if to == "" {
		return nil, errors.New("reply recipient is missing")
	}
	content := draftContent{Channel: channel, To: to, From: from}
	if channel == channelEmail && convo.Subject != "" {
		content.Subject = "Re: " + strings.TrimPrefix(convo.Subject, "Re: ")
	}
	content, err = patchDraftContent(content, args)
	if err != nil {
		return nil, err
	}
	key := strArg(args, "client_key")
	if key == "" {
		key = uuid.NewString()
	}
	if len(key) > 200 {
		return nil, errors.New("client_key too long")
	}
	raw, _ := json.Marshal(content)
	now := draftNow()
	author := draftAuthor(args)
	res, err := ctx.AppDB().Exec(`INSERT INTO conversation_drafts(project_id,contact_id,conversation_id,reply_to_activity_id,content,client_key,created_by,updated_by,created_at,updated_at,messaging_install_id) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,client_key) DO NOTHING`, pid, convo.ContactID, convo.ID, anchor, string(raw), key, author, author, now, now, messagingInstallID(ctx))
	if err != nil {
		return nil, err
	}
	var id int64
	err = ctx.AppDB().QueryRow(`SELECT id FROM conversation_drafts WHERE project_id=? AND client_key=?`, pid, key).Scan(&id)
	if err != nil {
		return nil, err
	}
	d, err := getDraft(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if d.ContactID != convo.ContactID || d.ConversationID != convo.ID {
		return nil, errors.New("client_key already used for another conversation")
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		draftChanged(ctx, pid, d)
	}
	return map[string]any{"draft": d}, nil
}

func (a *App) toolDraftGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := getDraft(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"draft": d}, nil
}

func (a *App) toolDraftList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	convo, err := dbConversationGet(ctx.AppDB(), pid, int64Arg(args, "conversation_id"))
	if err != nil {
		return nil, err
	}
	if convo == nil {
		return nil, errors.New("conversation not found in this project")
	}
	limit := intArg(args, "limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := intArg(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	where := `project_id=? AND conversation_id=?`
	params := []any{pid, convo.ID}
	if !boolArg(args, "include_finished", false) {
		where += ` AND status IN ('draft','sending','send_failed')`
	}
	var total int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM conversation_drafts WHERE `+where, params...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,contact_id,conversation_id,status,revision,created_by,updated_by,updated_at,last_error,json_extract(content,'$.channel'),json_extract(content,'$.from'),json_extract(content,'$.to'),json_extract(content,'$.subject'),substr(json_extract(content,'$.body'),1,200) FROM conversation_drafts WHERE `+where+` ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?`, append(params, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	drafts := []map[string]any{}
	for rows.Next() {
		var id, cid, conv, revision int64
		var status, createdBy, updatedBy, updatedAt, lastError, channel, from, to, subject, preview string
		if err = rows.Scan(&id, &cid, &conv, &status, &revision, &createdBy, &updatedBy, &updatedAt, &lastError, &channel, &from, &to, &subject, &preview); err != nil {
			return nil, err
		}
		drafts = append(drafts, map[string]any{"id": id, "contact_id": cid, "conversation_id": conv, "status": status, "revision": revision, "created_by": createdBy, "updated_by": updatedBy, "updated_at": updatedAt, "last_error": lastError, "channel": channel, "from": from, "to": to, "subject": subject, "preview": preview})
	}
	return map[string]any{"drafts": drafts, "total": total, "limit": limit, "offset": offset, "content_included": false}, rows.Err()
}

func (a *App) toolDraftUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := getDraft(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if d.Status != "draft" || int64Arg(args, "expected_revision") != d.Revision {
		return nil, errDraftConflict
	}
	for key, want := range map[string]int64{"contact_id": d.ContactID, "conversation_id": d.ConversationID, "reply_to_activity_id": d.ReplyToActivityID} {
		if _, ok := args[key]; ok && int64Arg(args, key) != want {
			return nil, errors.New("draft reply identity cannot change")
		}
	}
	content, err := patchDraftContent(d.Content, args)
	if err != nil {
		return nil, err
	}
	if err = validateReplyTransport(d.Content.Channel, content.Channel); err != nil {
		return nil, err
	}
	if content.Channel != d.Content.Channel {
		// Channel switches must explicitly choose their sender; no inherited WA
		// identity can silently become an SMS sender.
		if _, ok := args["from"]; !ok {
			content.From = ""
		}
	}
	raw, _ := json.Marshal(content)
	res, err := ctx.AppDB().Exec(`UPDATE conversation_drafts SET content=?,revision=revision+1,updated_by=?,updated_at=?,last_error='',attempt_key='' WHERE project_id=? AND id=? AND revision=? AND status='draft'`, string(raw), draftAuthor(args), draftNow(), pid, d.ID, d.Revision)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, errDraftConflict
	}
	d, err = getDraft(ctx.AppDB(), pid, d.ID)
	if err != nil {
		return nil, err
	}
	draftChanged(ctx, pid, d)
	return map[string]any{"draft": d}, nil
}

func (a *App) toolDraftDiscard(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(`UPDATE conversation_drafts SET status='discarded',revision=revision+1,updated_by=?,updated_at=? WHERE project_id=? AND id=? AND revision=? AND status='draft'`, draftAuthor(args), draftNow(), pid, int64Arg(args, "id"), int64Arg(args, "expected_revision"))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, errDraftConflict
	}
	d, err := getDraft(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	draftChanged(ctx, pid, d)
	return map[string]any{"draft": d}, nil
}

func (a *App) draftPreflight(ctx *sdk.AppCtx, pid string, d *conversationDraft) error {
	convo, err := dbConversationGet(ctx.AppDB(), pid, d.ConversationID)
	if err != nil {
		return err
	}
	if convo == nil || convo.ContactID != d.ContactID {
		return errors.New("draft conversation ownership changed")
	}
	if err = validateReplyTransport(convo.Channel, d.Content.Channel); err != nil {
		return err
	}
	to, receiving, err := replyRoute(ctx.AppDB(), pid, d.ContactID, d.ConversationID, d.Content.Channel, d.ReplyToActivityID)
	if err != nil {
		return err
	}
	if to != d.Content.To {
		return errors.New("reply recipient changed; draft was not sent")
	}
	source := messagingInstallID(ctx)
	if source == 0 {
		return errors.New("Messaging is not bound")
	}
	if d.MessagingInstallID > 0 && d.MessagingInstallID != source {
		return errors.New("Messaging binding changed; create a new draft after reviewing its sender and recipient")
	}
	if d.Content.From == "" {
		return errors.New("choose a verified sender before sending this draft")
	}
	if err = verifyReplySender(ctx, pid, d.Content.Channel, d.Content.From); err != nil {
		return err
	}
	if d.Content.Channel == channelEmail && receiving != "" {
		_, reason, err := validateInboundDelivery(ctx, pid, &inboundPayload{Channel: channelEmail, MatchedRecipient: receiving})
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("reply receiving identity is no longer owned: %s", reason)
		}
	}
	if d.Content.Channel == channelWhatsApp && d.Content.TemplateID == 0 && d.Content.ContentSID == "" {
		window, err := a.checkWhatsAppSession(ctx, pid, d.Content.From, d.Content.To)
		if err != nil {
			return err
		}
		if window["active"] != true {
			return errors.New("WhatsApp reply window is closed; save an approved template or explicitly choose SMS before sending")
		}
	}
	return nil
}

func (a *App) toolDraftSend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := getDraft(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if d.Status == "sent" {
		return map[string]any{"draft": d, "sent": true, "deduped": true, "result": d.Result}, nil
	}
	if int64Arg(args, "expected_revision") != d.Revision {
		return nil, errDraftConflict
	}
	if d.Status == "discarded" {
		return nil, errDraftConflict
	}
	if d.Status == "sending" {
		until, _ := time.Parse(time.RFC3339Nano, d.LeaseUntil)
		if time.Now().Before(until) {
			return nil, errors.New("draft send is already in progress; do not create another send")
		}
	}
	if d.AttemptKey == "" {
		d.AttemptKey = "crm-draft:" + uuid.NewString()
	}
	now := draftNow()
	lease := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	res, err := ctx.AppDB().Exec(`UPDATE conversation_drafts SET status='sending',revision=revision+1,attempt_key=?,lease_until=?,updated_at=? WHERE project_id=? AND id=? AND revision=? AND status IN ('draft','send_failed','sending')`, d.AttemptKey, lease, now, pid, d.ID, d.Revision)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, errDraftConflict
	}
	d.Revision++
	dispatched := d.Dispatched
	definiteFailure := false
	var out any
	// Recover a crash after recording delivery but before recording the draft
	// outcome. No new dispatch is needed, even if send eligibility changed.
	act, sendErr := dbActivityByIdempotencyKey(ctx.AppDB(), pid, d.AttemptKey, "")
	if sendErr == nil && act != nil {
		if act.ContactID != d.ContactID || act.ConversationID != d.ConversationID {
			sendErr = errors.New("draft delivery identity mismatch")
		} else {
			out = outboundSendResult(act, d.Content.Channel, d.Content.To, activityProviderMessageID(act), d.AttemptKey, true)
		}
	} else if sendErr == nil {
		sendErr = a.draftPreflight(ctx, pid, d)
	}
	if sendErr == nil && act == nil {
		content, _ := json.Marshal(d.Content)
		sendArgs := map[string]any{}
		_ = json.Unmarshal(content, &sendArgs)
		if d.Content.Channel == channelWhatsApp && (d.Content.TemplateID > 0 || d.Content.ContentSID != "") {
			// Keep freeform notes in the draft, but do not mix them into an
			// explicitly selected WhatsApp template send.
			sendArgs["body"] = ""
			sendArgs["body_html"] = ""
		}
		delete(sendArgs, "to")
		sendArgs["id"] = d.ContactID
		sendArgs["conversation_id"] = d.ConversationID
		sendArgs["reply_to_activity_id"] = d.ReplyToActivityID
		sendArgs["_project_id"] = pid
		sendArgs["idempotency_key"] = d.AttemptKey
		sendArgs["_draft_expected_to"] = d.Content.To
		sendArgs["_draft_before_dispatch"] = func() error {
			if messagingInstallID(ctx) != d.MessagingInstallID && d.MessagingInstallID > 0 {
				return errors.New("Messaging binding changed")
			}
			r, e := ctx.AppDB().Exec(`UPDATE conversation_drafts SET dispatched=1,messaging_install_id=? WHERE project_id=? AND id=? AND revision=? AND status='sending'`, messagingInstallID(ctx), pid, d.ID, d.Revision)
			if e != nil {
				return e
			}
			n, _ := r.RowsAffected()
			if n != 1 {
				return errDraftConflict
			}
			dispatched = true
			return nil
		}
		sendArgs["_draft_dispatch_outcome"] = func(response map[string]any, callErr error) {
			if callErr == nil {
				switch strings.ToLower(strings.TrimSpace(anyString(response["status"]))) {
				case "failed", "rejected", "suppressed", "rendering_failed":
					definiteFailure = true
				}
			}
		}
		out, sendErr = a.toolReply(ctx, sendArgs)
	}
	state := "sent"
	lastError := ""
	var result any
	if sendErr != nil {
		state = "draft"
		lastError = sendErr.Error()
		if dispatched && !definiteFailure {
			state = "send_failed"
			lastError = "Delivery outcome is uncertain. Retry this same saved draft; editing/discarding is locked to prevent duplicate delivery. " + lastError
		}
	} else {
		raw, e := json.Marshal(out)
		if e != nil {
			return nil, e
		}
		result = string(raw)
	}
	// Retain the exact idempotency key if a delivery outcome is uncertain.
	key := d.AttemptKey
	if state == "draft" {
		key = ""
		dispatched = false
	}
	res, err = ctx.AppDB().Exec(`UPDATE conversation_drafts SET status=?,revision=revision+1,lease_until='',last_error=?,sent_result=?,attempt_key=?,dispatched=?,updated_at=? WHERE project_id=? AND id=? AND revision=? AND status='sending'`, state, lastError, result, key, dispatched, draftNow(), pid, d.ID, d.Revision)
	if err != nil {
		return nil, fmt.Errorf("draft outcome persistence failed; retry this same draft: %w", err)
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return nil, errDraftConflict
	}
	d, err = getDraft(ctx.AppDB(), pid, d.ID)
	if err != nil {
		return nil, err
	}
	draftChanged(ctx, pid, d)
	// Return the preserved draft on failure so UI/agents have the fresh revision.
	return map[string]any{"draft": d, "sent": sendErr == nil, "error": lastError, "result": out}, nil
}

func (a *App) handleHTTPDrafts(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/drafts"), "/"), "/")
	args := map[string]any{"_project_id": pid}
	if r.Method != "GET" {
		args, err = mustReadJSONArgsLimit(w, r, maxMessageJSONBodyBytes)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args["_project_id"] = pid
	}
	var out any
	if parts[0] == "" {
		switch r.Method {
		case "GET":
			for _, key := range []string{"conversation_id", "limit", "offset"} {
				if value := r.URL.Query().Get(key); value != "" {
					args[key] = value
				}
			}
			args["include_finished"] = r.URL.Query().Get("include_finished") == "true"
			out, err = a.toolDraftList(globalCtx, args)
		case "POST":
			out, err = a.toolDraftCreate(globalCtx, args)
		default:
			httpErr(w, 405, "GET or POST only")
			return
		}
	} else {
		id, parseErr := strconv.ParseInt(parts[0], 10, 64)
		if parseErr != nil || id < 1 {
			httpErr(w, 400, "draft id required")
			return
		}
		args["id"] = id
		if len(parts) > 1 {
			if len(parts) != 2 || parts[1] != "send" || r.Method != "POST" {
				httpErr(w, 405, "POST /drafts/<id>/send only")
				return
			}
			out, err = a.toolDraftSend(globalCtx, args)
		} else {
			switch r.Method {
			case "GET":
				out, err = a.toolDraftGet(globalCtx, args)
			case "PATCH":
				out, err = a.toolDraftUpdate(globalCtx, args)
			case "DELETE":
				out, err = a.toolDraftDiscard(globalCtx, args)
			default:
				httpErr(w, 405, "GET, PATCH or DELETE only")
				return
			}
		}
	}
	if err != nil {
		status := 400
		if errors.Is(err, errDraftConflict) {
			status = 409
		}
		if errors.Is(err, errDraftNotFound) {
			status = 404
		}
		httpErr(w, status, err.Error())
		return
	}
	httpJSON(w, out)
}
