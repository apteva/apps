package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Only fill missing inbound content. A retry must never replace a complete
// message, create another activity, reopen a thread, or replay business events.
func repairMissingInboundBodyTx(tx *sql.Tx, pid string, contactID, activityID int64, body inboundPayload) (bool, error) {
	if body.Channel != channelEmail || strings.TrimSpace(inboundMessageText(body)) == "" {
		return false, nil
	}
	var stored string
	err := tx.QueryRow(`SELECT COALESCE(body,'') FROM contact_activities
		WHERE project_id=? AND contact_id=? AND id=? AND messaging_id=?
		AND kind='email_received' AND source='messaging'`, pid, contactID, activityID, body.MessageID).Scan(&stored)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if text := strings.TrimSpace(stored); text != "" && text != strings.TrimSpace(body.Subject) {
		return false, nil
	}
	result, err := tx.Exec(`UPDATE contact_activities SET body=?
		WHERE project_id=? AND contact_id=? AND id=? AND messaging_id=? AND COALESCE(body,'')=?`,
		inboundActivityBody(body), pid, contactID, activityID, body.MessageID, stored)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

// Explicit recovery also works for legacy activities whose Messaging install
// was not recorded. The RFC Message-ID must match the original in the currently
// bound Messaging install before any local write is allowed.
func (a *App) toolRefreshMessageBody(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	contactID, activityID := int64Arg(args, "id"), int64Arg(args, "activity_id")
	if contactID <= 0 || activityID <= 0 {
		return nil, errors.New("id and activity_id required")
	}
	bound := messagingBound(ctx)
	if bound == nil {
		return nil, errors.New("Messaging app must be bound to recover the original message")
	}
	var messageID, sourceID int64
	var header, stored, sourceDetail string
	err = ctx.AppDB().QueryRow(`SELECT COALESCE(messaging_id,0),COALESCE(messaging_install_id,0),
		COALESCE(message_id_header,''),COALESCE(body,''),COALESCE(source_detail,'') FROM contact_activities
		WHERE project_id=? AND contact_id=? AND id=? AND kind='email_received' AND source='messaging'`,
		pid, contactID, activityID).Scan(&messageID, &sourceID, &header, &stored, &sourceDetail)
	if err == sql.ErrNoRows {
		return nil, errors.New("inbound email activity not found in this contact and project")
	}
	if err != nil {
		return nil, err
	}
	if messageID <= 0 || (sourceID != 0 && sourceID != bound.InstallID) {
		return nil, errors.New("activity does not belong to the bound Messaging install")
	}
	var original struct {
		Message *struct {
			ID              int64  `json:"id"`
			ProjectID       string `json:"project_id"`
			Channel         string `json:"channel"`
			Direction       string `json:"direction"`
			Subject         string `json:"subject"`
			From            string `json:"from"`
			BodyText        string `json:"body_text"`
			BodyHTML        string `json:"body_html"`
			MessageIDHeader string `json:"message_id_header"`
		} `json:"message"`
	}
	if err = ctx.WithProject(pid).PlatformAPI().CallAppResult("messaging", "message_get", map[string]any{"id": messageID}, &original); err != nil {
		return nil, err
	}
	m := original.Message
	if m == nil || m.ID != messageID || m.ProjectID != pid || m.Direction != "in" || m.Channel != channelEmail {
		return nil, errors.New("original inbound email not found in this project")
	}
	normalizeHeader := func(value string) string { return strings.Trim(strings.TrimSpace(value), "<>") }
	if normalizeHeader(header) == "" || normalizeHeader(header) != normalizeHeader(m.MessageIDHeader) {
		return nil, errors.New("original RFC Message-ID does not match the CRM activity")
	}
	body := inboundPayload{MessageID: m.ID, Channel: m.Channel, Subject: m.Subject, BodyText: m.BodyText, BodyHTML: m.BodyHTML}
	missing := strings.TrimSpace(stored) == "" || strings.TrimSpace(stored) == strings.TrimSpace(m.Subject)
	newBody := inboundActivityBody(body)
	bodyRecoverable := missing && strings.TrimSpace(inboundMessageText(body)) != ""
	normalizeFormatting, _ := args["normalize_formatting"].(bool)
	recoverSender, _ := args["recover_sender"].(bool)
	formattingRecoverable := false
	if normalizeFormatting && !missing && strings.TrimSpace(m.BodyText) == "" && strings.TrimSpace(m.BodyHTML) != "" {
		legacyBody := legacyPlainTextFromHTML(m.BodyHTML)
		if m.Subject != "" {
			legacyBody = m.Subject + "\n\n" + legacyBody
		}
		formattingRecoverable = stored == legacyBody && newBody != stored && strings.TrimSpace(inboundMessageText(body)) != ""
	}
	newDetail := sourceDetail
	senderRecoverable := false
	if recoverSender && inboundEmailRecipient(m.From) != "" {
		detail := map[string]json.RawMessage{}
		if strings.TrimSpace(sourceDetail) == "" || json.Unmarshal([]byte(sourceDetail), &detail) == nil {
			if detail != nil { // Preserve malformed/null audit metadata instead of replacing it.
				current, exists := detail["from"]
				var from string
				if !exists || (json.Unmarshal(current, &from) == nil && strings.TrimSpace(from) == "") {
					detail["from"], _ = json.Marshal(inboundEmailRecipient(m.From))
					encoded, encodeErr := json.Marshal(detail)
					if encodeErr != nil {
						return nil, encodeErr
					}
					newDetail, senderRecoverable = string(encoded), true
				}
			}
		}
	}
	recoverable := bodyRecoverable || formattingRecoverable || senderRecoverable
	dryRun := true
	if value, ok := args["dry_run"].(bool); ok {
		dryRun = value
	}
	out := map[string]any{"contact_id": contactID, "activity_id": activityID, "messaging_id": messageID,
		"dry_run": dryRun, "recoverable": recoverable, "body_repaired": false,
		"formatting_recoverable": formattingRecoverable, "formatting_repaired": false,
		"sender_recoverable": senderRecoverable, "sender_repaired": false}
	if dryRun || !recoverable {
		return out, nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Recheck provenance after the provider read, including concurrent edits.
	var unchanged int
	err = tx.QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE project_id=? AND contact_id=? AND id=?
		AND messaging_id=? AND messaging_install_id=? AND COALESCE(message_id_header,'')=? AND COALESCE(body,'')=?
		AND COALESCE(source_detail,'')=?`,
		pid, contactID, activityID, messageID, sourceID, header, stored, sourceDetail).Scan(&unchanged)
	if err != nil {
		return nil, err
	}
	if unchanged != 1 {
		return nil, errors.New("activity changed during recovery; read it again before retrying")
	}
	if !bodyRecoverable && !formattingRecoverable {
		newBody = stored
	}
	result, err := tx.Exec(`UPDATE contact_activities SET body=?,source_detail=CASE WHEN ? THEN ? ELSE source_detail END
		WHERE project_id=? AND contact_id=? AND id=? AND messaging_id=? AND messaging_install_id=?
		AND kind='email_received' AND source='messaging' AND COALESCE(message_id_header,'')=?
		AND COALESCE(body,'')=? AND COALESCE(source_detail,'')=?`, newBody, senderRecoverable, newDetail,
		pid, contactID, activityID, messageID, sourceID, header, stored, sourceDetail)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, errors.New("activity changed during recovery; read it again before retrying")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	out["body_repaired"] = bodyRecoverable || formattingRecoverable
	out["formatting_repaired"] = formattingRecoverable
	out["sender_repaired"] = senderRecoverable
	return out, nil
}
