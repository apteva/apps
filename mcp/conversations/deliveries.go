package main

// deliveries.go — the ledger's store half. A delivery row exists for
// every (message, target) before the first attempt, so a crash between
// append and send is recoverable: pending rows replay on mount.

import "time"

type Delivery struct {
	ID          int64      `json:"id"`
	MessageID   int64      `json:"message_id"`
	Target      string     `json:"target"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

func (s *store) EnsureDelivery(messageID int64, target string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO deliveries (message_id, target) VALUES (?, ?)`,
		messageID, target)
	return err
}

func (s *store) MarkDelivered(messageID int64, target string) error {
	_, err := s.db.Exec(`
		UPDATE deliveries SET status = 'delivered', attempts = attempts + 1,
			last_error = '', delivered_at = CURRENT_TIMESTAMP
		WHERE message_id = ? AND target = ?`, messageID, target)
	return err
}

// RecordDeliveryError keeps the row pending when the failure is
// recoverable (unconfigured binding, transient network) and marks it
// failed when terminal. Pending rows replay on mount; failed rows are
// an operator-visible dead letter, never retried silently.
func (s *store) RecordDeliveryError(messageID int64, target, message string, terminal bool) error {
	status := "pending"
	if terminal {
		status = "failed"
	}
	_, err := s.db.Exec(`
		UPDATE deliveries SET status = ?, attempts = attempts + 1, last_error = ?
		WHERE message_id = ? AND target = ?`, status, message, messageID, target)
	return err
}

func (s *store) PendingDeliveries(limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT id, message_id, target, status, attempts, last_error
		FROM deliveries WHERE status = 'pending' ORDER BY id ASC LIMIT ?`, limit)
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
