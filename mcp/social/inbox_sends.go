// inbox_sends — durable send intents around inbox_reply. Each attempt is
// stored before the provider call, so a retry with the same idempotency
// key replays the result instead of posting twice, an identical reply is
// refused while the first is in flight or just sent, and a send that
// timed out stays "uncertain" until sync finds our reply upstream.
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	inboxSendPending   = "pending"
	inboxSendSent      = "sent"
	inboxSendFailed    = "failed"
	inboxSendUncertain = "uncertain"

	// Without a key, an identical reply to the same item inside this
	// window is treated as a duplicate of the earlier one.
	inboxDuplicateWindow = 10 * time.Minute
	// Open intents older than this are no longer matched by sync.
	inboxReconcileWindow = 48 * time.Hour
)

type inboxSendIntent struct {
	ID             int64
	InboxItemID    int64
	IdempotencyKey string
	RequestHash    string
	State          string
	ExternalID     string
	Outcome        string
	Error          string
	CreatedAt      string
}

// inboxSendRequestHash identifies what is being sent: the text and the
// attachments, not the transport arguments.
func inboxSendRequestHash(itemID int64, args map[string]any) string {
	canonical := map[string]any{"item": itemID}
	for key, value := range args {
		switch key {
		case "id", "idempotency_key", "project_id", "_project_id", "media_project_id", "storage_project_id":
			continue
		}
		if key == "body" {
			value = normalizeInboxText(toString(value))
		}
		canonical[key] = value
	}
	raw, _ := json.Marshal(canonical) // map keys marshal sorted
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizeInboxText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func scanInboxSendIntent(row *sql.Row) (*inboxSendIntent, error) {
	var in inboxSendIntent
	var key, outcome, errText sql.NullString
	err := row.Scan(&in.ID, &in.InboxItemID, &key, &in.RequestHash, &in.State, &in.ExternalID, &outcome, &errText, &in.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.IdempotencyKey, in.Outcome, in.Error = key.String, outcome.String, errText.String
	return &in, nil
}

const inboxSendIntentColumns = `id, inbox_item_id, idempotency_key, request_hash, state, external_id, outcome_json, error, created_at`

func (a *App) toolInboxReply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	pid := projectScope(ctx, args)
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return mcpError("id required"), nil
	}
	item, err := getInboxItem(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
	}
	if item == nil {
		return mcpError("inbox item not found"), nil
	}
	db := ctx.AppDB()
	key := strings.TrimSpace(toString(args["idempotency_key"]))
	if len(key) > 200 {
		return mcpError("idempotency_key must be at most 200 characters"), nil
	}
	hash := inboxSendRequestHash(item.ID, args)
	base := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, IdempotencyKey: key}

	var intentID int64
	if key != "" {
		prior, err := scanInboxSendIntent(db.QueryRow(`SELECT `+inboxSendIntentColumns+` FROM inbox_send_intents WHERE project_id=? AND idempotency_key=?`, pid, key))
		if err != nil {
			return nil, err
		}
		if prior != nil {
			if prior.InboxItemID != item.ID || prior.RequestHash != hash {
				return mcpError("idempotency_key was already used for a different reply"), nil
			}
			if prior.State != inboxSendFailed {
				return replayInboxSend(base, prior), nil
			}
			// A definite failure sent nothing: retry under the same intent.
			if _, err := db.Exec(`UPDATE inbox_send_intents SET state='pending', error=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='failed'`, prior.ID); err != nil {
				return nil, err
			}
			intentID = prior.ID
		}
	} else {
		prior, err := scanInboxSendIntent(db.QueryRow(`SELECT `+inboxSendIntentColumns+` FROM inbox_send_intents
			WHERE project_id=? AND inbox_item_id=? AND request_hash=?
			  AND (state IN ('pending','uncertain') OR (state='sent' AND created_at >= ?))
			ORDER BY id DESC LIMIT 1`, pid, item.ID, hash, time.Now().UTC().Add(-inboxDuplicateWindow).Format("2006-01-02 15:04:05")))
		if err != nil {
			return nil, err
		}
		if prior != nil && (prior.State == inboxSendSent || withinInboxReconcileWindow(prior.CreatedAt)) {
			out := replayInboxSend(base, prior)
			out.Status = "skipped"
			out.Reason = fmt.Sprintf("duplicate of reply %d (%s); pass a new idempotency_key to send it again", prior.ID, prior.State)
			return out, nil
		}
	}
	if intentID == 0 {
		root := item.ExternalID
		if item.ParentExternalID != "" {
			root = item.ParentExternalID
		}
		res, err := db.Exec(`INSERT INTO inbox_send_intents
			(project_id, inbox_item_id, social_account_id, platform, kind, target_external_id, target_root_id, target_thread_id, idempotency_key, request_hash, body_text)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			pid, item.ID, item.SocialAccountID, item.Platform, item.Kind, item.ExternalID, root, item.ExternalPostID, nullableStr(key), hash, normalizeInboxText(toString(args["body"])))
		if err != nil {
			if key != "" && strings.Contains(strings.ToLower(err.Error()), "unique") {
				// A concurrent call with the same key won the insert.
				prior, _ := scanInboxSendIntent(db.QueryRow(`SELECT `+inboxSendIntentColumns+` FROM inbox_send_intents WHERE project_id=? AND idempotency_key=?`, pid, key))
				if prior != nil {
					return replayInboxSend(base, prior), nil
				}
			}
			return nil, fmt.Errorf("record send intent: %w", err)
		}
		intentID, _ = res.LastInsertId()
	}

	result, err := a.inboxReplyOnce(ctx, args)
	if err != nil {
		_, _ = db.Exec(`UPDATE inbox_send_intents SET state=?, error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, inboxSendUncertain, err.Error(), intentID)
		return nil, err
	}
	out, ok := result.(inboxOutcome)
	if !ok {
		// Validation errors returned before any provider call.
		_, _ = db.Exec(`UPDATE inbox_send_intents SET state=?, error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, inboxSendFailed, "rejected before sending", intentID)
		return result, nil
	}
	state := inboxSendStateFor(out)
	out.SendID, out.SendState, out.IdempotencyKey = intentID, state, key
	if state == inboxSendUncertain {
		out.Reason = strings.TrimSpace(out.Reason + " The provider did not confirm the send; Social will match it on the next sync. Retry with the same idempotency_key to read the result without sending twice.")
	}
	outcomeJSON, _ := json.Marshal(out)
	topic, event := inboxSendEvent(item, out)
	_, _ = db.Exec(`UPDATE inbox_send_intents SET state=?, external_id=?, outcome_json=?, error=?, updated_at=CURRENT_TIMESTAMP, `+queueEventSQL+` WHERE id=?`,
		state, out.ExternalID, string(outcomeJSON), nullableStr(out.Error), topic, event, intentID)
	publishRowEvent(ctx, durableInboxSends, intentID)
	return out, nil
}

// inboxSendStateFor maps a handler outcome to a send state. Unsupported
// and skipped outcomes never reached the provider.
func inboxSendStateFor(out inboxOutcome) string {
	switch out.Status {
	case "ok", "partial":
		return inboxSendSent
	case "failed":
		if inboxSendUncertainError(out.Error) {
			return inboxSendUncertain
		}
	}
	return inboxSendFailed
}

// inboxSendUncertainError reports transport failures after which the
// provider may or may not have created the reply.
func inboxSendUncertainError(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"timeout", "timed out", "deadline exceeded", "context canceled", "connection reset", "broken pipe", "eof", "status 502", "status 503", "status 504", "http 502", "http 503", "http 504", "bad gateway", "gateway timeout"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func withinInboxReconcileWindow(createdAt string) bool {
	created, err := time.Parse("2006-01-02 15:04:05", createdAt)
	return err != nil || time.Since(created) < inboxReconcileWindow
}

// replayInboxSend returns the stored result of an earlier attempt.
func replayInboxSend(base inboxOutcome, prior *inboxSendIntent) inboxOutcome {
	out := base
	if prior.Outcome != "" {
		_ = json.Unmarshal([]byte(prior.Outcome), &out)
	}
	out.SendID, out.SendState, out.Replayed = prior.ID, prior.State, true
	if prior.IdempotencyKey != "" {
		out.IdempotencyKey = prior.IdempotencyKey
	}
	if prior.ExternalID != "" {
		out.ExternalID = prior.ExternalID
	}
	switch prior.State {
	case inboxSendPending:
		out.Status, out.Reason = "skipped", "this reply is still being sent"
	case inboxSendUncertain:
		out.Status, out.Reason = "skipped", "the provider did not confirm this reply; Social will match it on the next sync"
	case inboxSendSent:
		if out.Status == "" {
			out.Status = "ok"
		}
	}
	return out
}

// inboxSendEvent is the reply_sent / reply_failed / reply_uncertain event
// queued with the send's final state.
func inboxSendEvent(item *inboxItem, out inboxOutcome) (string, string) {
	topic := inboxEventReplyFailed
	switch out.SendState {
	case inboxSendSent:
		topic = inboxEventReplySent
	case inboxSendUncertain:
		topic = inboxEventReplyUnsure
	}
	payload := map[string]any{
		"send_id":           out.SendID,
		"inbox_item_id":     item.ID,
		"social_account_id": item.SocialAccountID,
		"platform":          item.Platform,
		"kind":              item.Kind,
		"status":            out.Status,
	}
	for key, value := range map[string]string{"idempotency_key": out.IdempotencyKey, "external_id": out.ExternalID, "permalink": out.Permalink, "reason": out.Reason, "error": out.Error} {
		if value != "" {
			payload[key] = value
		}
	}
	return queuedEvent(topic, payload)
}

// reconcileInboxSend settles an open intent when sync finds our own reply
// upstream: same account and target, and the same text. Returns the
// settled intent, or nil when the reply was not sent through Social.
func reconcileInboxSend(db sqlRunner, in inboxUpsertInput, itemID int64) (*inboxSendIntent, error) {
	rows, err := db.Query(`SELECT id, project_id, inbox_item_id, COALESCE(idempotency_key,''), body_text, target_external_id, target_root_id, target_thread_id, created_at
		FROM inbox_send_intents
		WHERE social_account_id=? AND kind=? AND state IN ('pending','uncertain') AND created_at >= ?
		ORDER BY id`, in.SocialAccountID, in.Kind, time.Now().UTC().Add(-inboxReconcileWindow).Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id, itemID                       int64
		pid, key, body, target, root, th string
	}
	var match *candidate
	body := normalizeInboxText(in.Body)
	for rows.Next() {
		var c candidate
		var created string
		if rows.Scan(&c.id, &c.pid, &c.itemID, &c.key, &c.body, &c.target, &c.root, &c.th, &created) != nil {
			continue
		}
		sameTarget := in.ParentExternalID != "" && (in.ParentExternalID == c.target || in.ParentExternalID == c.root)
		if in.Kind == inboxKindDM && c.th != "" && in.ExternalPostID == c.th {
			sameTarget = true
		}
		if sameTarget && c.body == body {
			match = &c
			break
		}
	}
	rows.Close()
	if match == nil {
		return nil, nil
	}
	out := inboxOutcome{InboxItemID: match.itemID, SocialAccountID: in.SocialAccountID, Platform: in.Platform, Status: "ok", ExternalID: in.ExternalID, Permalink: in.Permalink, SendID: match.id, SendState: inboxSendSent, IdempotencyKey: match.key, Reason: "confirmed by sync"}
	outcomeJSON, _ := json.Marshal(out)
	payload := map[string]any{
		"send_id":           match.id,
		"inbox_item_id":     match.itemID,
		"reply_item_id":     itemID,
		"social_account_id": in.SocialAccountID,
		"platform":          in.Platform,
		"kind":              in.Kind,
		"status":            "ok",
		"external_id":       in.ExternalID,
		"reconciled":        true,
	}
	if match.key != "" {
		payload["idempotency_key"] = match.key
	}
	topic, event := queuedEvent(inboxEventReplySent, payload)
	res, err := db.Exec(`UPDATE inbox_send_intents SET state='sent', external_id=?, outcome_json=?, error=NULL, updated_at=CURRENT_TIMESTAMP, `+queueEventSQL+` WHERE id=? AND state IN ('pending','uncertain')`, in.ExternalID, string(outcomeJSON), topic, event, match.id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	if err := markInboxRepliedByExternalID(db, in.SocialAccountID, in.Kind, match.target); err != nil {
		return nil, err
	}
	return &inboxSendIntent{ID: match.id, InboxItemID: match.itemID, IdempotencyKey: match.key, State: inboxSendSent, ExternalID: in.ExternalID}, nil
}
