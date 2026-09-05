package main

import (
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strings"
)

// Recovery clears delivery evidence only. Messaging suppressions remain in
// force until independently removed at their source.
func recoverChannelDelivery(ctx *sdk.AppCtx, pid string, channelID int64, transport, reason string) error {
	if channelID <= 0 || transportChannelKind(transport) == "" || strings.TrimSpace(reason) == "" {
		return errors.New("channel_id, valid transport, and recovery reason are required")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var contactID int64
	var evidence sql.NullString
	if err = tx.QueryRow(`SELECT ch.contact_id,ds.delivery_evidence FROM contact_channel_delivery_state ds JOIN contact_channels ch ON ch.id=ds.channel_id AND ch.project_id=ds.project_id WHERE ds.project_id=? AND ds.channel_id=? AND ds.transport=?`, pid, channelID, transport).Scan(&contactID, &evidence); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO crm_delivery_recoveries(project_id,channel_id,transport,reason,previous_evidence) VALUES(?,?,?,?,?)`, pid, channelID, transport, reason, evidence); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE contact_channel_delivery_state SET delivery_evidence=NULL,status='active',status_reason=?,status_updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),consecutive_soft_bounces=0,quarantined=0,quarantined_at=NULL WHERE project_id=? AND channel_id=? AND transport=?`, reason, pid, channelID, transport); err != nil {
		return err
	}
	if err = queueCRMEvent(tx, pid, "contact.channel.deliverability.changed", map[string]any{"contact_id": contactID, "channel_id": channelID, "transport": transport, "recovery_reason": reason}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = deliverQueuedCRMEvents(ctx); err != nil {
		ctx.Logger().Warn("recovery event pending", "err", err)
	}
	return nil
}
func (a *App) handleHTTPDeliveryRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var body map[string]any
	if err = decodeJSONBody(w, r, &body); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err = recoverChannelDelivery(globalCtx, pid, int64Arg(body, "channel_id"), strArg(body, "transport"), strArg(body, "reason")); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, map[string]any{"recovered": true})
}
