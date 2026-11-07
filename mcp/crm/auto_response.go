package main

import (
	"database/sql"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// A correction can arrive before the original event. Keep a tombstone so that
// late original notifications cannot reintroduce the disproved soft bounce.
func correctAutomaticResponse(tx *sql.Tx, pid string, channelID int64, transport string, event sdk.Event, state *deliveryStateRow) (bool, error) {
	original := strings.TrimSpace(anyString(event.Data["reclassified_event_id"]))
	if original == "" {
		return false, nil
	}
	var active int
	err := tx.QueryRow(`SELECT active_soft FROM crm_delivery_event_contributions WHERE project_id=? AND source_install_id=? AND event_id=? AND channel_id=? AND transport=?`, pid, event.SourceInstallID, original, channelID, transport).Scan(&active)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	_, err = tx.Exec(`INSERT INTO crm_delivery_event_contributions(project_id,source_install_id,event_id,channel_id,transport,occurred_at,corrected)
 VALUES(?,?,?,?,?,?,1) ON CONFLICT(project_id,source_install_id,event_id) DO UPDATE SET active_soft=0,corrected=1`, pid, event.SourceInstallID, original, channelID, transport, anyString(event.Data["occurred_at"]))
	if err != nil || active == 0 || (state.Status != "active" && state.Status != "soft_bounced") {
		return false, err
	}
	count := max(0, state.ConsecutiveSoftBounces-1)
	status := "soft_bounced"
	if count == 0 {
		status = "active"
	}
	var last, reason sql.NullString
	err = tx.QueryRow(`SELECT occurred_at,'transient-bounce' FROM crm_delivery_event_contributions WHERE project_id=? AND channel_id=? AND transport=? AND active_soft=1 ORDER BY julianday(occurred_at) DESC LIMIT 1`, pid, channelID, transport).Scan(&last, &reason)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	// For a pre-ledger soft-bounce history, keep its reason if it still contributes.
	if count > 0 && !reason.Valid {
		reason = sql.NullString{String: "transient-bounce", Valid: true}
	}
	_, err = tx.Exec(`UPDATE contact_channel_delivery_state SET status=?,consecutive_soft_bounces=?,quarantined=?,quarantined_at=CASE WHEN ?=0 THEN NULL ELSE quarantined_at END,last_bounce_at=CASE WHEN ? IS NULL AND ?>0 THEN last_bounce_at ELSE ? END,status_reason=?,updated_at=CURRENT_TIMESTAMP
 WHERE project_id=? AND channel_id=? AND transport=?`, status, count, boolToInt(count >= softBounceQuarantineThreshold), boolToInt(count >= softBounceQuarantineThreshold), last, count, last, reason, pid, channelID, transport)
	remove := count < softBounceQuarantineThreshold && state.Suppressed && state.SuppressionSource == "crm" && state.SuppressionReason == "soft-bounce-threshold"
	return remove, err
}
