package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// drainEvents publishes committed changes in sequence, retrying the same ID
// after a lost acknowledgement. Delivery is at least once; subscribers must
// deduplicate by event_id. One project's outage does not stall other projects.
func (a *App) drainEvents(ctx context.Context) error {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	api := a.ctx.EventBusAPI()
	if api == nil {
		return errors.New("app event publisher unavailable")
	}
	rows, err := a.db.QueryContext(ctx, `SELECT sequence,event_id,project_id,topic,payload_json,occurred_at,attempts,next_attempt_at FROM process_event_outbox e WHERE NOT EXISTS (SELECT 1 FROM process_event_outbox earlier WHERE earlier.project_id=e.project_id AND earlier.sequence<=e.sequence AND earlier.next_attempt_at>?) ORDER BY sequence LIMIT 200`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type pending struct {
		sequence                        int64
		id, project, topic, payload, at string
		attempts                        int
		next                            string
	}
	var batch []pending
	for rows.Next() {
		var e pending
		if err = rows.Scan(&e.sequence, &e.id, &e.project, &e.topic, &e.payload, &e.at, &e.attempts, &e.next); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	blocked := map[string]bool{}
	var failures []error
	for _, e := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		if blocked[e.project] {
			continue
		}
		if next, _ := time.Parse(time.RFC3339Nano, e.next); next.After(time.Now()) {
			blocked[e.project] = true
			continue
		}
		data := map[string]any{}
		err = json.Unmarshal([]byte(e.payload), &data)
		if err == nil {
			data["schema_version"] = 1
			data["event_id"] = e.id
			// Monotonic installation-wide change revision, including dispatch changes
			// that do not alter the entity's optimistic edit revision.
			data["revision"] = e.sequence
			data["occurred_at"] = e.at
			err = api.PublishAppEvent(e.project, e.id, e.topic, data)
		}
		if err != nil {
			blocked[e.project] = true
			delay := time.Second * time.Duration(1<<min(e.attempts, 6))
			_, saveErr := a.db.ExecContext(ctx, `UPDATE process_event_outbox SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE sequence=?`, time.Now().Add(delay).UTC().Format(time.RFC3339Nano), err.Error(), e.sequence)
			failures = append(failures, errors.Join(fmt.Errorf("publish %s: %w", e.topic, err), saveErr))
			continue
		}
		if _, err = a.db.ExecContext(ctx, `DELETE FROM process_event_outbox WHERE sequence=?`, e.sequence); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
