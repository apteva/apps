package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type emailUnsubscribeState struct {
	ProjectID       string `json:"project_id"`
	ContactID       int64  `json:"contact_id"`
	ConversationID  int64  `json:"conversation_id"`
	Address         string `json:"address"`
	OutboundBlocked bool   `json:"outbound_blocked"`
	InboundBlocked  bool   `json:"inbound_blocked"`
	Unsubscribed    bool   `json:"unsubscribed"`
	Direction       string `json:"direction,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Confirmed       bool   `json:"confirmed"`
	ChannelID       int64  `json:"-"`
}

// Never derive an unsubscribe target from To/Reply-To, the primary contact
// address, or accumulated participants. Pin the latest inbound message's From
// and require that exact email channel on the same project's active contact.
func emailUnsubscribeTarget(db *sql.DB, pid string, cid, expectedContact int64) (*emailUnsubscribeState, error) {
	convo, err := dbConversationGet(db, pid, cid)
	if err != nil {
		return nil, err
	}
	if convo == nil || (expectedContact > 0 && convo.ContactID != expectedContact) {
		return nil, errors.New("conversation not found for this contact and project")
	}
	if convo.Channel != channelEmail {
		return nil, errors.New("unsubscribe email requires an email conversation")
	}
	var detail string
	err = db.QueryRow(`SELECT COALESCE(source_detail,'') FROM contact_activities
		WHERE project_id=? AND contact_id=? AND conversation_id=? AND kind='email_received'
		ORDER BY julianday(occurred_at) DESC,id DESC LIMIT 1`, pid, convo.ContactID, cid).Scan(&detail)
	if err == sql.ErrNoRows {
		return nil, errors.New("no inbound email identifies an unsubscribe target")
	}
	if err != nil {
		return nil, err
	}
	addresses := messageAddresses(ActivityKindEmailReceived, detail)
	if addresses == nil {
		return nil, errors.New("inbound sender metadata is missing; cannot safely unsubscribe")
	}
	address := inboundEmailRecipient(addresses.From)
	if address == "" {
		return nil, errors.New("inbound sender email is missing or invalid; cannot safely unsubscribe")
	}
	state := &emailUnsubscribeState{ProjectID: pid, ContactID: convo.ContactID, ConversationID: cid, Address: address}
	err = db.QueryRow(`SELECT ch.id FROM contact_channels ch JOIN contacts c ON c.id=ch.contact_id AND c.project_id=ch.project_id
		WHERE ch.project_id=? AND ch.contact_id=? AND ch.kind='email' AND lower(ch.value)=?
		AND c.deleted_at IS NULL AND c.status!='merged'`, pid, convo.ContactID, address).Scan(&state.ChannelID)
	if err == sql.ErrNoRows {
		return nil, errors.New("inbound sender is not an email channel of this contact")
	}
	return state, err
}

func directionalEmailSuppression(ctx *sdk.AppCtx, pid, address, direction string) (suppressionCheckResult, error) {
	var result suppressionCheckResult
	if err := callMessagingTool(ctx, "suppression_check", map[string]any{"_project_id": pid, "channel": "email", "address": address, "direction": direction}, &result); err != nil {
		return result, err
	}
	// Old Messaging silently ignores unknown args. Capability proof is required
	// BEFORE writing, not merely a check after a legacy bidirectional block.
	if result.CheckDirection != direction {
		return result, errors.New("Messaging v0.13.59 or newer is required for safe outbound-only unsubscribe; upgrade the bound Messaging app first")
	}
	return result, nil
}

func loadEmailUnsubscribeState(ctx *sdk.AppCtx, state *emailUnsubscribeState) (suppressionCheckResult, error) {
	out, err := directionalEmailSuppression(ctx, state.ProjectID, state.Address, "outbound")
	if err != nil {
		return out, err
	}
	in, err := directionalEmailSuppression(ctx, state.ProjectID, state.Address, "inbound")
	if err != nil {
		return out, err
	}
	state.OutboundBlocked = out.Suppressed
	state.InboundBlocked = in.Suppressed
	state.Direction = out.Direction
	state.Reason = out.Reason
	state.Unsubscribed = out.Suppressed && statusForSuppression(out.Reason) == "unsubscribed"
	return out, nil
}

func (a *App) toolUnsubscribeEmail(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	state, err := emailUnsubscribeTarget(ctx.AppDB(), pid, int64Arg(args, "conversation_id"), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if _, err = loadEmailUnsubscribeState(ctx, state); err != nil {
		return nil, err
	}
	if boolArg(args, "dry_run", true) {
		return state, nil
	}
	if strArg(args, "expected_address") != state.Address {
		return nil, errors.New("expected_address must exactly match the preview; reload if the inbound sender changed")
	}
	var added struct {
		Suppression struct {
			Direction string `json:"direction"`
		} `json:"suppression"`
	}
	if err = callMessagingTool(ctx, "suppression_add", map[string]any{"_project_id": pid, "channel": "email", "kind": "address", "address": state.Address, "direction": "outbound", "reason": "unsubscribe", "source": "crm"}, &added); err != nil {
		return nil, fmt.Errorf("unsubscribe was not confirmed; Messaging may have applied it, refresh or retry safely: %w", err)
	}
	if added.Suppression.Direction != "outbound" && added.Suppression.Direction != "both" {
		return nil, errors.New("Messaging did not confirm directional suppression; refresh before retrying")
	}
	check, err := loadEmailUnsubscribeState(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("unsubscribe may have been applied but readback failed; refresh or retry safely: %w", err)
	}
	if !check.Suppressed {
		return nil, errors.New("Messaging readback did not confirm the unsubscribe; no success recorded")
	}
	// If the sender changed during the remote call, do not silently apply local
	// state to another channel. The pinned address remains blocked in Messaging.
	current, err := emailUnsubscribeTarget(ctx.AppDB(), pid, state.ConversationID, state.ContactID)
	if err != nil || current.Address != state.Address {
		return nil, errors.New("the pinned email was blocked but the conversation changed; reload before continuing")
	}
	source := strings.TrimSpace(strArg(args, "source"))
	if source == "" {
		source = "agent"
	}
	key := fmt.Sprintf("email-unsubscribe:%d:%s", state.ChannelID, check.SuppressedAt)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	status := statusForSuppression(check.Reason)
	updated, err := tx.Exec(`UPDATE contact_channel_delivery_state SET suppressed=1,suppression_kind=?,suppression_match=?,suppression_reason=?,suppression_source=?,suppressed_at=?,suppression_checked_at=?,
		status=CASE WHEN delivery_evidence IS NOT NULL THEN delivery_evidence WHEN ?<>'' THEN ? ELSE status END,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND channel_id=? AND transport='email'`, check.Kind, check.Matched, check.Reason, check.Source, nullStr(check.SuppressedAt), now, status, status, pid, state.ChannelID)
	if err != nil {
		return nil, err
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return nil, errors.New("email was blocked in Messaging but local eligibility could not be confirmed; refresh before retrying")
	}
	var existing int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE project_id=? AND idempotency_key=?`, pid, key).Scan(&existing); err != nil {
		return nil, err
	}
	var act *Activity
	if existing == 0 {
		// This is an operator audit, not a sent/received message. Preserve
		// interaction timestamps and the conversation's latest message recency.
		detail, _ := json.Marshal(map[string]any{"address": state.Address, "direction": state.Direction, "inbound_blocked": state.InboundBlocked, "reason": "unsubscribe"})
		body := "Email unsubscribe requested and outbound block confirmed for " + state.Address + ". Existing inbound blocks, if any, were preserved."
		inserted, insertErr := tx.Exec(`INSERT INTO contact_activities (project_id,contact_id,conversation_id,kind,body,occurred_at,source,source_detail,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?)`, pid, state.ContactID, state.ConversationID, ActivityKindSystem, body, now, source, string(detail), key)
		err = insertErr
		if err != nil {
			return nil, err
		}
		id, err := inserted.LastInsertId()
		if err != nil {
			return nil, err
		}
		act = &Activity{ID: id, Kind: ActivityKindSystem, Source: source}
	}
	if _, err = tx.Exec(`UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE project_id=? AND id=?`, pid, state.ContactID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	state.Confirmed = true
	emitChannelDeliverabilityChanged(ctx, pid, state.ContactID, state.ChannelID, "email")
	if act != nil {
		emitCRMEvent(ctx, pid, "contact.activity.added", map[string]any{"contact_id": state.ContactID, "activity_id": act.ID, "conversation_id": state.ConversationID, "kind": act.Kind, "source": act.Source})
	}
	return state, nil
}

func (a *App) handleHTTPUnsubscribeEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	parts := contactsPathParts(r)
	if len(parts) != 4 {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{"_project_id": pid, "id": parts[0], "conversation_id": parts[2], "dry_run": true}
	if r.Method == http.MethodPost {
		var body map[string]any
		if err = decodeJSONBody(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		args["dry_run"] = false
		args["expected_address"] = body["expected_address"]
		args["source"] = "human"
	}
	out, err := a.toolUnsubscribeEmail(getAppCtx(r), args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}
