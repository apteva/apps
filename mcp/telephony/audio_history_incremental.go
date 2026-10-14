package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

var errHistoryCursor = errors.New("invalid history cursor")

type carrierHistoryCursor struct {
	Scope    [32]byte
	Sequence int64
}
type carrierHistoryPage struct {
	Records []carrierTelemetryRecord `json:"records"`
	Cursor  string                   `json:"next_cursor"`
	More    bool                     `json:"has_more"`
	Reset   bool                     `json:"reset_required"`
}

func (c *callsDB) carrierHistoryPage(ctx context.Context, project, call, text string) (carrierHistoryPage, error) {
	page := carrierHistoryPage{Records: []carrierTelemetryRecord{}}
	cursor := carrierHistoryCursor{Scope: sha256.Sum256([]byte(project + "\x00" + call))}
	if text != "" {
		if len(text) > 512 {
			return page, errHistoryCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(text)
		var incoming carrierHistoryCursor
		if err != nil || json.Unmarshal(raw, &incoming) != nil || incoming.Scope != cursor.Scope || incoming.Sequence < 0 {
			return page, errHistoryCursor
		}
		cursor = incoming
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Persistence sequence, not provider occurrence time: a late observation
	// must remain discoverable after an operator advanced the cursor.
	var oldest int64
	err := c.db.QueryRowContext(ctx, `SELECT seq FROM telephony_carrier_history_cursors WHERE project_id=? AND call_id=? ORDER BY seq LIMIT 1`, project, call).Scan(&oldest)
	if err != nil && err != sql.ErrNoRows {
		return page, err
	}
	if text != "" && cursor.Sequence > 0 && ((oldest > cursor.Sequence) || err == sql.ErrNoRows) {
		page.Reset = true
	}
	rows, err := c.db.QueryContext(ctx, `SELECT s.seq,h.evidence_json FROM telephony_carrier_history_cursors s JOIN telephony_carrier_transport_history h ON h.id=s.evidence_id WHERE s.project_id=? AND s.call_id=? AND s.seq>? AND h.expires_at>? ORDER BY s.seq LIMIT 257`, project, call, cursor.Sequence, ringTime(time.Now()))
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var raw string
		var r carrierTelemetryRecord
		if err = rows.Scan(&seq, &raw); err != nil {
			return page, err
		}
		if len(page.Records) == 256 {
			page.More = true
			break
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return page, err
		}
		page.Records = append(page.Records, r)
		cursor.Sequence = seq
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	raw, _ := json.Marshal(cursor)
	page.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	return page, nil
}
