package main

import (
	"database/sql"
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
	var header, stored string
	err = ctx.AppDB().QueryRow(`SELECT COALESCE(messaging_id,0),COALESCE(messaging_install_id,0),
		COALESCE(message_id_header,''),COALESCE(body,'') FROM contact_activities
		WHERE project_id=? AND contact_id=? AND id=? AND kind='email_received' AND source='messaging'`,
		pid, contactID, activityID).Scan(&messageID, &sourceID, &header, &stored)
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
			BodyText        string `json:"body_text"`
			BodyHTML        string `json:"body_html"`
			MessageIDHeader string `json:"message_id_header"`
		} `json:"message"`
	}
	if err = ctx.WithProject(pid).PlatformAPI().CallAppResult(bound.AppSlug, "message_get", map[string]any{"id": messageID}, &original); err != nil {
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
	recoverable := missing && strings.TrimSpace(inboundMessageText(body)) != ""
	dryRun := true
	if value, ok := args["dry_run"].(bool); ok {
		dryRun = value
	}
	out := map[string]any{"contact_id": contactID, "activity_id": activityID, "messaging_id": messageID,
		"dry_run": dryRun, "recoverable": recoverable, "body_repaired": false}
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
		AND messaging_id=? AND messaging_install_id=? AND COALESCE(message_id_header,'')=? AND COALESCE(body,'')=?`,
		pid, contactID, activityID, messageID, sourceID, header, stored).Scan(&unchanged)
	if err != nil {
		return nil, err
	}
	if unchanged != 1 {
		return nil, errors.New("activity changed during recovery; read it again before retrying")
	}
	repaired, err := repairMissingInboundBodyTx(tx, pid, contactID, activityID, body)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	out["body_repaired"] = repaired
	return out, nil
}
