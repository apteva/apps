// durable_events — bus events that must not be lost. The statement that
// changes a row's state also stores the event on that row (event_topic,
// event_payload); it is published right after the write and cleared only
// once the platform acknowledges it. The event_publisher worker resends
// whatever is still pending after a failed publish or a restart, under
// the same event ID, so the platform drops duplicates.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// sqlRunner is satisfied by *sql.DB and *sql.Tx.
type sqlRunner interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// durableTable is a table whose rows carry their own pending event.
type durableTable struct {
	name    string
	prefix  string // event ID: social:<prefix>:<row id>:<event_seq>
	project string // SQL expression for the row's project
}

var (
	durableInboxItems  = durableTable{name: "inbox_items", prefix: "inbox", project: "project_id"}
	durableInboxSends  = durableTable{name: "inbox_send_intents", prefix: "send", project: "project_id"}
	durablePostTargets = durableTable{name: "post_targets", prefix: "target", project: "(SELECT p.project_id FROM posts p WHERE p.id=post_targets.post_id)"}
	durableTables      = []durableTable{durableInboxItems, durableInboxSends, durablePostTargets}
)

const (
	// queueEventSQL goes in the SET clause of the state change; it takes
	// the topic and the payload JSON from queuedEvent, in that order.
	queueEventSQL = `event_seq=event_seq+1, event_topic=?, event_payload=?, event_attempts=0, event_next_at=NULL`

	durableEventBatch       = 100
	durableEventMaxBackoff  = time.Hour
	durableEventSendTimeout = 10 * time.Second
)

func queuedEvent(topic string, payload map[string]any) (string, string) {
	raw, _ := json.Marshal(payload)
	return topic, string(raw)
}

func durableEventID(table durableTable, rowID, seq int64) string {
	return fmt.Sprintf("social:%s:%d:%d", table.prefix, rowID, seq)
}

type pendingEvent struct {
	table     durableTable
	rowID     int64
	seq       int64
	attempts  int
	projectID string
	topic     string
	payload   string
}

// publishRowEvent publishes the row's pending event now, if it has one.
// A failure leaves it for the event_publisher worker.
func publishRowEvent(ctx *sdk.AppCtx, table durableTable, rowID int64) {
	if rowID <= 0 {
		return
	}
	events, err := loadPendingEvents(ctx.AppDB(), table, "id=?", rowID)
	if err != nil {
		return
	}
	for _, event := range events {
		_ = deliverPendingEvent(ctx, event)
	}
}

// runEventPublisher resends events whose earlier publish failed or never
// ran, oldest first, backing off per row.
func (a *App) runEventPublisher(runCtx context.Context, ctx *sdk.AppCtx) error {
	return publishPendingEvents(runCtx, ctx)
}

func publishPendingEvents(runCtx context.Context, ctx *sdk.AppCtx) error {
	now := time.Now().UTC().Format(inboxEventTimestampForm)
	pid := projectScope(ctx)
	var failed error
	for _, table := range durableTables {
		where, args := "(event_next_at IS NULL OR event_next_at<=?)", []any{now}
		if pid != "" {
			where += " AND " + table.project + "=?"
			args = append(args, pid)
		}
		events, err := loadPendingEvents(ctx.AppDB(), table, where, args...)
		if err != nil {
			return err
		}
		for _, event := range events {
			if runCtx.Err() != nil {
				return runCtx.Err()
			}
			if err := deliverPendingEvent(ctx, event); err != nil {
				failed = err
			}
		}
	}
	return failed
}

func loadPendingEvents(db sqlRunner, table durableTable, where string, args ...any) ([]pendingEvent, error) {
	rows, err := db.Query(`SELECT id, event_seq, event_attempts, COALESCE(`+table.project+`,''), event_topic, COALESCE(event_payload,'{}')
		FROM `+table.name+` WHERE event_topic IS NOT NULL AND `+where+` ORDER BY id LIMIT ?`, append(args, durableEventBatch)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingEvent
	for rows.Next() {
		event := pendingEvent{table: table}
		if err := rows.Scan(&event.rowID, &event.seq, &event.attempts, &event.projectID, &event.topic, &event.payload); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// deliverPendingEvent publishes one event and clears it on acknowledgment.
// The clear is guarded by event_seq: a newer event queued meanwhile stays.
func deliverPendingEvent(ctx *sdk.AppCtx, event pendingEvent) error {
	db := ctx.AppDB()
	err := publishDurableEvent(ctx, event)
	if err == nil {
		_, err = db.Exec(`UPDATE `+event.table.name+` SET event_topic=NULL, event_payload=NULL, event_attempts=0, event_next_at=NULL WHERE id=? AND event_seq=?`, event.rowID, event.seq)
		return err
	}
	backoff := time.Duration(1<<min(event.attempts, 12)) * time.Minute
	if backoff > durableEventMaxBackoff {
		backoff = durableEventMaxBackoff
	}
	next := time.Now().UTC().Add(backoff).Format(inboxEventTimestampForm)
	_, _ = db.Exec(`UPDATE `+event.table.name+` SET event_attempts=event_attempts+1, event_next_at=? WHERE id=? AND event_seq=?`, next, event.rowID, event.seq)
	ctx.Logger().Warn("event_publisher: publish failed", "topic", event.topic, "event_id", durableEventID(event.table, event.rowID, event.seq), "attempt", event.attempts+1, "err", err)
	return err
}

// publishDurableEvent sends the event with its stable ID and returns only
// once the platform has accepted it.
func publishDurableEvent(ctx *sdk.AppCtx, event pendingEvent) error {
	if event.projectID == "" {
		return errors.New("event has no project")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(event.payload), &data); err != nil {
		return fmt.Errorf("decode event payload: %w", err)
	}
	id := durableEventID(event.table, event.rowID, event.seq)
	if bus := ctx.EventBusAPI(); bus != nil {
		return bus.PublishAppEvent(event.projectID, id, event.topic, data)
	}
	// Platforms without the event bus API still acknowledge emits.
	data["event_id"] = id
	sendCtx, cancel := context.WithTimeout(context.Background(), durableEventSendTimeout)
	defer cancel()
	return ctx.EmitWithProjectAck(sendCtx, event.topic, event.projectID, data)
}
