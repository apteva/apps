package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The outbox is committed with the business transition. Its immutable source
// and payload are retried through Core's idempotent event endpoint.
func enqueueDeliveryTx(tx *sql.Tx, task *Task, target, eventType, key string, at time.Time) error {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	payload := taskWakePayload(task, target, eventType)
	source := "notification:" + key
	switch eventType {
	case "task.ready", "task.recovery.ready":
		source = taskAgentEventSourceID(task.ID)
	case "task.terminalization_required":
		source = taskTerminalizationSourceID(task.ID)
		payload = map[string]any{"type": eventType, "origin": "tasks.lifecycle", "task_id": task.ID, "occurrence_id": task.ID, "assigned_thread_id": target, "reply_required": false,
			"required_first_action": map[string]any{"tool": "tasks_get", "task_id": task.ID},
			"parent_task_id":        task.ParentTaskID, "reply_thread_id": nil, "instruction": "This is a reconciliation-only event. First call tasks_get. Do not call send; do not repeat an ambiguous external action. Reconcile durable evidence; complete only when verified, otherwise fail with external_outcome_unknown."}
	case "task.terminal":
		payload = map[string]any{"type": eventType, "task_id": task.ID, "title": task.Title, "state": task.State, "result": task.Result, "error": task.Error, "parent_task_id": task.ParentTaskID, "attention_required": task.State == stateFailed}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO task_deliveries
  (id,task_id,project_id,agent_id,event_type,target_thread_id,source_event_id,payload_json,created_at,next_attempt_at)
  VALUES (?,?,?,?,?,?,?,?,?,?)`, key, task.ID, task.ProjectID, task.AgentID, eventType, target, source, string(raw), at.Format(timeFormat), at.Format(timeFormat))
	return err
}

func enqueueCreatedTaskTx(tx *sql.Tx, task *Task, at time.Time) error {
	if task.ScheduledFor != nil {
		eventType := "task.ready"
		if task.RecoveryOfTaskID != "" {
			eventType = "task.recovery.ready"
		}
		return enqueueDeliveryTx(tx, task, task.AssignedThreadID, eventType, "execution:"+task.ID, at)
	}
	if !isScheduleDefinitionTask(task) && !terminalState(task.State) && task.AssignedThreadID != task.CreatedByThreadID {
		return enqueueDeliveryTx(tx, task, task.AssignedThreadID, "task.assigned", "created:"+task.ID, at)
	}
	return nil
}

func enqueueTerminalTx(tx *sql.Tx, task *Task, force bool, reason string, at time.Time) error {
	target := task.CreatedByThreadID
	if force && target == "" {
		target = task.AssignedThreadID
	}
	if !force && target == task.AssignedThreadID {
		return nil
	}
	if err := enqueueDeliveryTx(tx, task, target, "task.terminal", "terminal:"+task.ID, at); err != nil {
		return err
	}
	if reason != "" {
		_, err := tx.Exec(`UPDATE task_deliveries SET payload_json=json_set(payload_json,'$.reason',?) WHERE id=?`, reason, "terminal:"+task.ID)
		return err
	}
	return nil
}

type pendingDelivery struct {
	ID, TaskID, Type, Target, Source, Payload string
	AgentID                                   int64
	Attempts                                  int
}

func (a *App) drainDeliveries(taskID, projectID string, at time.Time) error {
	query := `SELECT id,task_id,event_type,target_thread_id,source_event_id,payload_json,agent_id,attempts FROM task_deliveries WHERE delivered_at IS NULL AND next_attempt_at<=? AND (lease_until IS NULL OR lease_until<=?)`
	args := []any{at.Format(timeFormat), at.Format(timeFormat)}
	if taskID != "" {
		query += ` AND task_id=?`
		args = append(args, taskID)
	}
	if projectID != "" {
		query += ` AND project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY next_attempt_at,id LIMIT 64`
	rows, err := a.store.db.Query(query, args...)
	if err != nil {
		return err
	}
	var deliveries []pendingDelivery
	for rows.Next() {
		var d pendingDelivery
		if err := rows.Scan(&d.ID, &d.TaskID, &d.Type, &d.Target, &d.Source, &d.Payload, &d.AgentID, &d.Attempts); err != nil {
			rows.Close()
			return err
		}
		deliveries = append(deliveries, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	slots := make(chan struct{}, 4)
	for _, d := range deliveries {
		slots <- struct{}{}
		wg.Add(1)
		go func(d pendingDelivery) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := a.deliver(d, at); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(d)
	}
	wg.Wait()
	return errors.Join(failures...)
}

func (a *App) deliver(d pendingDelivery, at time.Time) error {
	lease := at.Add(2 * time.Minute).Format(timeFormat)
	result, err := a.store.db.Exec(`UPDATE task_deliveries SET lease_until=?, attempts=attempts+1 WHERE id=? AND delivered_at IS NULL AND next_attempt_at<=? AND (lease_until IS NULL OR lease_until<=?)`, lease, d.ID, at.Format(timeFormat), at.Format(timeFormat))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	task, err := a.store.Get(d.TaskID)
	if err == nil && d.Type != "task.terminal" && terminalState(task.State) {
		return a.finishDelivery(d.ID, at, nil)
	}
	if err == nil && d.Type == "task.assigned" && task.AssignedThreadID != d.Target {
		return a.finishDelivery(d.ID, at, nil)
	}
	if err == nil && (d.Type == "task.ready" || d.Type == "task.recovery.ready") && task.AcceptedAt != nil {
		return a.finishDelivery(d.ID, at, nil)
	}
	if err == nil && (d.Type == "task.ready" || d.Type == "task.recovery.ready") && task.DispatchedAt == nil {
		task, _, err = a.store.MarkDispatched(task.ID, "tasks:delivery", at)
	}
	if err == nil {
		if a.ctx == nil || a.ctx.AgentEventsAPI() == nil {
			err = errors.New("tracked platform event API unavailable")
		} else {
			var payload any
			if err = json.Unmarshal([]byte(d.Payload), &payload); err == nil {
				var receipt *sdk.AgentEventReceipt
				receipt, err = a.ctx.AgentEventsAPI().SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: d.AgentID, ThreadID: d.Target, SourceEventID: d.Source, Message: payload})
				if err == nil && (receipt == nil || receipt.ExecutionID == "") {
					err = errors.New("tracked event omitted execution_id")
				}
				if err == nil {
					switch d.Type {
					case "task.ready", "task.recovery.ready":
						err = a.store.RecordAgentEventExecution(d.TaskID, d.Source, receipt.ExecutionID, d.Target, at)
					case "task.terminalization_required":
						err = a.store.RecordTerminalizationExecution(d.TaskID, d.Source, receipt.ExecutionID, d.Target, at)
					}
				}
			}
		}
	}
	if saveErr := a.finishDelivery(d.ID, at, err); saveErr != nil {
		return saveErr
	}
	if err != nil {
		a.logger().Warn("task delivery pending retry", "task_id", d.TaskID, "event", d.Type, "err", err)
		return fmt.Errorf("task %s committed; delivery queued for retry: %w", d.TaskID, err)
	}
	return nil
}

func (a *App) finishDelivery(id string, at time.Time, deliveryErr error) error {
	if deliveryErr == nil {
		_, err := a.store.db.Exec(`UPDATE task_deliveries SET delivered_at=?,lease_until=NULL,last_error='' WHERE id=?`, at.Format(timeFormat), id)
		return err
	}
	_, err := a.store.db.Exec(`UPDATE task_deliveries SET lease_until=NULL,last_error=?,next_attempt_at=? WHERE id=?`, deliveryErr.Error(), at.Add(10*time.Second).Format(timeFormat), id)
	return err
}

const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// Aggregate delivery health exposes retry pressure without payloads or thread IDs.
func (a *App) handleDeliveryHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	project := requestProjectID(r)
	if project == "" {
		http.Error(w, "project context required", 400)
		return
	}
	var pending, retrying int
	var oldest sql.NullString
	err := a.store.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(last_error<>''),0),MIN(created_at) FROM task_deliveries WHERE project_id=? AND delivered_at IS NULL`, project).Scan(&pending, &retrying, &oldest)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	var age float64
	if oldest.Valid {
		if at, err := time.Parse(time.RFC3339Nano, oldest.String); err == nil {
			age = a.store.now().Sub(at).Seconds()
			if age < 0 {
				age = 0
			}
		}
	}
	var overdue, unaccepted, dispatchFailures, terminalizationFailures int
	var oldestActive sql.NullString
	err = a.store.db.QueryRow(`SELECT
 COALESCE(SUM(schedule_enabled=1 AND next_run_at<?),0),
 COALESCE(SUM(dispatched_at IS NOT NULL AND accepted_at IS NULL AND state='queued'),0),
 COALESCE(SUM(state='failed' AND error LIKE 'dispatched but not accepted%'),0),
 COALESCE(SUM(state='failed' AND (error LIKE 'agent_exited_without_terminal_status%' OR error LIKE 'terminalization_execution_error%')),0),
 MIN(CASE WHEN state IN ('queued','running','waiting','blocked') AND NOT `+definitionPredicate+` THEN created_at END)
 FROM tasks WHERE project_id=?`, a.store.now().Format(timeFormat), project).Scan(&overdue, &unaccepted, &dispatchFailures, &terminalizationFailures, &oldestActive)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	var oldestActiveAge float64
	if oldestActive.Valid {
		if at, err := time.Parse(time.RFC3339Nano, oldestActive.String); err == nil {
			oldestActiveAge = a.store.now().Sub(at).Seconds()
			if oldestActiveAge < 0 {
				oldestActiveAge = 0
			}
		}
	}
	writeJSON(w, 200, map[string]any{"pending": pending, "retrying": retrying, "oldest_pending_seconds": age, "overdue_schedules": overdue, "unaccepted_dispatches": unaccepted, "dispatch_failures": dispatchFailures, "terminalization_failures": terminalizationFailures, "oldest_active_seconds": oldestActiveAge})
}
