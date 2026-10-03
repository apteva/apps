package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// asyncTaskNotificationsAvailable reports whether the platform will create
// the stream subscription declared for agent_ask. Older servers keep the
// direct Core delivery path; current servers route requester updates through
// the exact caller thread and the durable app-event outbox.
func asyncTaskNotificationsAvailable(app *sdk.AppCtx) bool {
	if app == nil || app.EventBusAPI() == nil {
		return false
	}
	info, err := app.PlatformInfo()
	if err != nil || info == nil || info.AsyncResultNotifications == nil {
		return false
	}
	for _, mode := range info.AsyncResultNotifications.Modes {
		if mode == "stream" {
			return info.AsyncResultNotifications.Version >= 1
		}
	}
	return false
}

// requesterReplyEvent preserves compatibility with older servers while
// preventing duplicate requester notifications when the async subscription is
// active. The A2A task row and message are still committed by saveReply.
func requesterReplyEvent(app *sdk.AppCtx, task *Task, toAgentID int64, event string) string {
	if task != nil && toAgentID == task.FromAgentID && asyncTaskNotificationsAvailable(app) {
		return ""
	}
	return event
}

// emitTask publishes the task lifecycle to the app-event bus. The event
// outbox is separate from a2a_deliveries: the task ledger remains authoritative
// while publication is retried independently with a stable event ID.
func emitTask(app *sdk.AppCtx, topic string, task *Task, message ...string) {
	if app == nil || task == nil {
		return
	}
	body := ""
	if len(message) > 0 {
		body = message[0]
	}
	payload := taskEventPayload(task, body)

	// A test harness or older server without EventBusAPI keeps the existing
	// best-effort UI event behavior.
	if app.EventBusAPI() == nil {
		app.EmitWithProject(topic, task.ProjectID, payload)
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		app.Logger().Warn("a2a task event marshal failed", "task", task.ID, "topic", topic, "err", err)
		return
	}
	eventID := stableTaskEventID(topic, raw)
	var rowID int64
	err = app.AppDB().QueryRow(`SELECT id FROM a2a_event_outbox WHERE event_id=?`, eventID).Scan(&rowID)
	if err == sql.ErrNoRows {
		result, insertErr := app.AppDB().Exec(`INSERT INTO a2a_event_outbox(project_id,event_id,topic,payload_json,next_attempt,created_at) VALUES(?,?,?,?,?,?)`,
			task.ProjectID, eventID, topic, string(raw), "", nowUTC())
		if insertErr != nil {
			app.Logger().Warn("a2a task event queue failed", "task", task.ID, "topic", topic, "err", insertErr)
			return
		}
		rowID, _ = result.LastInsertId()
	} else if err != nil {
		app.Logger().Warn("a2a task event lookup failed", "task", task.ID, "topic", topic, "err", err)
		return
	}
	publishTaskEventRow(context.Background(), app, rowID)

	// A terminal topic closes the server subscription. Keep task.updated as the
	// progress/input event and publish the exact terminal topic as well.
	if topic == "task.updated" {
		for _, terminal := range taskTerminalTopics(task.Status) {
			emitTaskTopic(app, terminal, task, payload)
		}
	}
}

func taskEventPayload(task *Task, message string) map[string]any {
	payload := map[string]any{
		"id":         task.ID,
		"task_id":    task.ID,
		"kind":       task.Kind,
		"status":     task.Status,
		"from":       task.FromAgentID,
		"to":         task.ToAgentID,
		"updated_at": task.UpdatedAt,
	}
	if strings.TrimSpace(message) != "" {
		payload["message"] = message
	}
	return payload
}

func taskTerminalTopics(status string) []string {
	switch status {
	case "completed":
		return []string{"task.completed"}
	case "failed":
		return []string{"task.failed"}
	case "canceled":
		return []string{"task.canceled"}
	default:
		return nil
	}
}

// queueTaskEventsTx is called by saveReply while the task lifecycle and reply
// message are still in one transaction. emitTask remains idempotent and will
// publish these rows after commit.
func queueTaskEventsTx(tx *sql.Tx, task *Task, message string) error {
	payload, err := json.Marshal(taskEventPayload(task, message))
	if err != nil {
		return err
	}
	topics := []string{"task.updated"}
	topics = append(topics, taskTerminalTopics(task.Status)...)
	for _, topic := range topics {
		eventID := stableTaskEventID(topic, payload)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO a2a_event_outbox(project_id,event_id,topic,payload_json,next_attempt,created_at) VALUES(?,?,?,?,?,?)`,
			task.ProjectID, eventID, topic, string(payload), "", nowUTC()); err != nil {
			return err
		}
	}
	return nil
}

func emitTaskTopic(app *sdk.AppCtx, topic string, task *Task, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	eventID := stableTaskEventID(topic, raw)
	var rowID int64
	err = app.AppDB().QueryRow(`SELECT id FROM a2a_event_outbox WHERE event_id=?`, eventID).Scan(&rowID)
	if err == sql.ErrNoRows {
		result, insertErr := app.AppDB().Exec(`INSERT INTO a2a_event_outbox(project_id,event_id,topic,payload_json,next_attempt,created_at) VALUES(?,?,?,?,?,?)`,
			task.ProjectID, eventID, topic, string(raw), "", nowUTC())
		if insertErr != nil {
			app.Logger().Warn("a2a terminal event queue failed", "task", task.ID, "topic", topic, "err", insertErr)
			return
		}
		rowID, _ = result.LastInsertId()
	} else if err != nil {
		return
	}
	publishTaskEventRow(context.Background(), app, rowID)
}

func stableTaskEventID(topic string, payload []byte) string {
	digest := sha256.Sum256(append([]byte(topic+"\x00"), payload...))
	return "a2a-" + hex.EncodeToString(digest[:])
}

func publishTaskEventRow(ctx context.Context, app *sdk.AppCtx, rowID int64) {
	if app == nil || rowID <= 0 || app.EventBusAPI() == nil {
		return
	}
	var project, eventID, topic, payload string
	var published int
	if err := app.AppDB().QueryRowContext(ctx, `SELECT project_id,event_id,topic,payload_json,published FROM a2a_event_outbox WHERE id=?`, rowID).Scan(&project, &eventID, &topic, &payload, &published); err != nil || published != 0 {
		return
	}
	var data any
	if json.Unmarshal([]byte(payload), &data) != nil {
		return
	}
	if err := app.EventBusAPI().PublishAppEvent(project, eventID, topic, data); err != nil {
		var attempts int
		_ = app.AppDB().QueryRow(`SELECT attempts FROM a2a_event_outbox WHERE id=?`, rowID).Scan(&attempts)
		attempts++
		delay := time.Second * time.Duration(1<<minInt(attempts-1, 5))
		_, _ = app.AppDB().Exec(`UPDATE a2a_event_outbox SET attempts=?,next_attempt=?,last_error=? WHERE id=?`, attempts, time.Now().UTC().Add(delay).Format(time.RFC3339Nano), truncateEventError(err.Error()), rowID)
		return
	}
	_, _ = app.AppDB().Exec(`UPDATE a2a_event_outbox SET published=1,last_error='' WHERE id=?`, rowID)
}

func flushTaskEvents(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.EventBusAPI() == nil {
		return nil
	}
	rows, err := app.AppDB().QueryContext(ctx, `SELECT id FROM a2a_event_outbox WHERE published=0 AND (next_attempt='' OR next_attempt<=?) ORDER BY id LIMIT 100`, nowUTC())
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		publishTaskEventRow(ctx, app, id)
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncateEventError(s string) string {
	if len(s) > 1000 {
		return s[:1000]
	}
	return fmt.Sprintf("%s", s)
}
