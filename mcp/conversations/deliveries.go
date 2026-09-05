package main

// deliveries.go — the ledger's store half. A delivery row exists for
// every (message, target) before the first attempt, so a crash between
// append and send is recoverable: pending rows replay on mount.

import (
	"fmt"
	"time"
)

type Delivery struct {
	ID          int64      `json:"id"`
	MessageID   int64      `json:"message_id"`
	Target      string     `json:"target"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

type FailedDelivery struct {
	Delivery
	ConversationID string `json:"conversation_id"`
	ProjectID      string `json:"project_id"`
}

func (s *store) EnsureDelivery(messageID int64, target string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO deliveries (message_id, target, next_attempt_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		messageID, target)
	return err
}

func (s *store) MarkDelivered(messageID int64, target string) error {
	_, err := s.db.Exec(`
		UPDATE deliveries SET status = 'delivered', attempts = attempts + 1,
			last_error = '', delivered_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE message_id = ? AND target = ?`, messageID, target)
	return err
}

// RecordDeliveryError keeps the row pending when the failure is
// recoverable (unconfigured binding, transient network) and marks it
// failed when terminal. Pending rows replay on mount; failed rows are
// an operator-visible dead letter, never retried silently.
func (s *store) RecordDeliveryError(messageID int64, target, message string, terminal bool) (bool, error) {
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM deliveries WHERE message_id=? AND target=?`, messageID, target).Scan(&attempts); err != nil {
		return false, err
	}
	attempts++
	failed := terminal || attempts >= 10
	status := "pending"
	if failed {
		status = "failed"
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	// 2s, 4s, ... capped at five minutes.
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	_, err := s.db.Exec(`
		UPDATE deliveries SET status = ?, attempts = ?, last_error = ?,
			next_attempt_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE message_id = ? AND target = ?`, status, attempts, message,
		time.Now().UTC().Add(delay).Format(time.RFC3339Nano), messageID, target)
	return failed, err
}

func (s *store) PendingDeliveries(limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT id, message_id, target, status, attempts, last_error
		FROM deliveries d
		WHERE status = 'pending' AND julianday(next_attempt_at) <= julianday(?)
        AND (d.target NOT LIKE 'telegram:%' OR NOT EXISTS(SELECT 1 FROM deliveries prior WHERE prior.target=d.target AND prior.message_id<d.message_id AND prior.status IN('pending','processing','ambiguous')))
		ORDER BY next_attempt_at ASC, id ASC LIMIT ?`, time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.MessageID, &d.Target, &d.Status, &d.Attempts, &d.LastError); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *store) DeliveryFor(messageID int64, target string) (*Delivery, error) {
	var d Delivery
	err := s.db.QueryRow(`
		SELECT id, message_id, target, status, attempts, last_error
		FROM deliveries WHERE message_id = ? AND target = ?`, messageID, target).
		Scan(&d.ID, &d.MessageID, &d.Target, &d.Status, &d.Attempts, &d.LastError)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *store) FailedDeliveries(projectID string, limit int) ([]FailedDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT d.id,d.message_id,d.target,d.status,d.attempts,d.last_error,m.conversation_id,c.project_id
		FROM deliveries d
		JOIN messages m ON m.id=d.message_id
		JOIN conversations c ON c.id=m.conversation_id
		WHERE c.project_id=? AND d.status IN ('failed','ambiguous')
		ORDER BY d.updated_at DESC,d.id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FailedDelivery{}
	for rows.Next() {
		var item FailedDelivery
		if err := rows.Scan(&item.ID, &item.MessageID, &item.Target, &item.Status, &item.Attempts,
			&item.LastError, &item.ConversationID, &item.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *store) RetryFailedDelivery(projectID string, deliveryID int64) error {
	res, err := s.db.Exec(`
		UPDATE deliveries SET status='pending',attempts=0,last_error='',
			next_attempt_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status IN ('failed','ambiguous') AND message_id IN (
			SELECT m.id FROM messages m JOIN conversations c ON c.id=m.conversation_id
			WHERE c.project_id=? AND c.archived_at IS NULL)`, deliveryID, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("failed delivery not found")
	}
	return nil
}
