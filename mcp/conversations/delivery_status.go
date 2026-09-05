package main

import (
	"net/http"
	"time"
)

func (a *App) handleDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", 405)
		return
	}
	id := r.URL.Query().Get("chat_id")
	if _, err := a.authorizeConversation(r, id); err != nil {
		http.Error(w, "conversation not found", 404)
		return
	}
	if r.URL.Query().Get("stats") == "1" {
		rows, err := a.store.db.Query(`SELECT d.target,d.status,COUNT(*),CAST(MAX((julianday('now')-julianday(m.created_at))*86400) AS INTEGER),COALESCE(AVG((julianday(d.delivered_at)-julianday(m.created_at))*86400),0)
        FROM deliveries d JOIN messages m ON m.id=d.message_id WHERE m.conversation_id=? AND d.target NOT LIKE 'web:%' GROUP BY d.target,d.status`, id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		type metric struct {
			Target                 string  `json:"target"`
			Status                 string  `json:"status"`
			Count                  int     `json:"count"`
			OldestAgeSeconds       int     `json:"oldest_age_seconds"`
			AverageDeliverySeconds float64 `json:"average_delivery_seconds"`
		}
		groups := []metric{}
		for rows.Next() {
			var group metric
			if err := rows.Scan(&group.Target, &group.Status, &group.Count, &group.OldestAgeSeconds, &group.AverageDeliverySeconds); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			groups = append(groups, group)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"groups": groups})
		return
	}
	rows, err := a.store.db.Query(`SELECT d.id,d.message_id,d.target,d.status,d.attempts,d.last_error FROM deliveries d JOIN messages m ON m.id=d.message_id WHERE m.conversation_id=? AND d.target NOT LIKE 'web:%' ORDER BY d.id DESC LIMIT 200`, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.MessageID, &d.Target, &d.Status, &d.Attempts, &d.LastError); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// Keep failures for operators, provider message links for deduplication, and at
// least the newest replay position for every message. Old intermediate edits
// and confirmed transport attempts are expendable after 30 days.
func (s *store) PruneDeliveryHistory() error {
	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM deliveries WHERE status='delivered' AND julianday(updated_at)<julianday(?) AND target NOT LIKE 'telegram:%'`, cutoff); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM message_changes WHERE julianday(created_at)<julianday(?) AND id NOT IN(SELECT MAX(id) FROM message_changes GROUP BY message_id)`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}
