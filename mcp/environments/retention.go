package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Retention is opt-in. Snapshots and definitions are never pruned.
func (s *service) pruneHistory() error {
	raw := strings.TrimSpace(os.Getenv("ENVIRONMENTS_RETENTION_DAYS"))
	if raw == "" || raw == "0" {
		return nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 36500 {
		return fmt.Errorf("ENVIRONMENTS_RETENTION_DAYS must be 0 or between 1 and 36500")
	}
	return s.pruneBefore(time.Now().UTC().AddDate(0, 0, -days))
}
func (s *service) pruneBefore(cutoff time.Time) error {
	rows, err := s.db.db.Query(`SELECT id FROM environment_runs r WHERE status IN ('stopped','failed','expired') AND stopped_at < ? AND NOT EXISTS (SELECT 1 FROM environment_voice_calls c WHERE c.run_id=r.id AND c.status='running') ORDER BY stopped_at LIMIT 100`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		calls, err := s.db.listVoiceCalls(id)
		if err != nil {
			return err
		}
		for _, call := range calls {
			for _, speaker := range []string{"receptionist", "caller", "caller-delivered"} {
				path, err := s.voiceRecordingPath(call.ID, speaker)
				if err != nil {
					return err
				}
				if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
		tx, err := s.db.db.Begin()
		if err != nil {
			return err
		}
		for _, table := range []string{"environment_voice_calls", "environment_web_fixture_events", "environment_web_fixtures", "environment_protocol_events", "environment_protocol_fixtures"} {
			if _, err = tx.Exec("DELETE FROM "+table+" WHERE run_id=?", id); err != nil {
				break
			}
		}
		if err == nil {
			_, err = tx.Exec("DELETE FROM environment_runs WHERE id=?", id)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
