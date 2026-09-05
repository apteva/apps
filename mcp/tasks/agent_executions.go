package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func scanTaskAgentExecution(row rowScanner) (*TaskAgentExecution, error) {
	var execution TaskAgentExecution
	var dispatched, updated string
	var deadline sql.NullString
	if err := row.Scan(&execution.SourceEventID, &execution.TaskID, &execution.Purpose,
		&execution.ExecutionID, &execution.ThreadID, &execution.State, &execution.Reason,
		&execution.Sequence, &dispatched, &updated, &deadline); err != nil {
		return nil, err
	}
	execution.DispatchedAt, _ = time.Parse(time.RFC3339Nano, dispatched)
	execution.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	execution.DeadlineAt = parseNullableTime(deadline)
	return &execution, nil
}

func upsertTaskAgentExecutionTx(tx *sql.Tx, execution TaskAgentExecution) error {
	var deadline any
	if execution.DeadlineAt != nil {
		deadline = execution.DeadlineAt.UTC().Format(timeFormat)
	}
	_, err := tx.Exec(`INSERT INTO task_agent_executions
		(source_event_id, task_id, purpose, execution_id, thread_id, state, reason,
		 sequence, dispatched_at, updated_at, deadline_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_event_id) DO UPDATE SET
			execution_id=CASE WHEN task_agent_executions.execution_id='' THEN excluded.execution_id ELSE task_agent_executions.execution_id END,
			thread_id=CASE WHEN task_agent_executions.thread_id='' THEN excluded.thread_id ELSE task_agent_executions.thread_id END,
			updated_at=CASE WHEN task_agent_executions.updated_at < excluded.updated_at THEN excluded.updated_at ELSE task_agent_executions.updated_at END`, execution.SourceEventID, execution.TaskID,
		execution.Purpose, execution.ExecutionID, execution.ThreadID, execution.State,
		execution.Reason, execution.Sequence, execution.DispatchedAt.UTC().Format(timeFormat),
		execution.UpdatedAt.UTC().Format(timeFormat), deadline)
	return err
}

func (s *taskStore) AgentExecutions(taskID string) ([]TaskAgentExecution, error) {
	rows, err := s.db.Query(`SELECT source_event_id, task_id, purpose, execution_id,
		thread_id, state, reason, sequence, dispatched_at, updated_at, deadline_at
		FROM task_agent_executions WHERE task_id=? ORDER BY dispatched_at, source_event_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskAgentExecution{}
	for rows.Next() {
		execution, scanErr := scanTaskAgentExecution(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *execution)
	}
	return out, rows.Err()
}

func (s *taskStore) RecordTerminalizationExecution(taskID, sourceID, executionID, threadID string, at time.Time) error {
	if strings.TrimSpace(taskID) == "" || sourceID != taskTerminalizationSourceID(taskID) || strings.TrimSpace(executionID) == "" {
		return errors.New("invalid terminalization execution mapping")
	}
	at = at.UTC()
	deadline := at.Add(agentSettleGrace)
	result, err := s.db.Exec(`UPDATE task_agent_executions SET
		execution_id=CASE WHEN execution_id='' THEN ? ELSE execution_id END,
		thread_id=CASE WHEN thread_id='' THEN ? ELSE thread_id END,
		updated_at=CASE WHEN sequence=0 THEN ? ELSE updated_at END, deadline_at=CASE WHEN sequence=0 THEN ? ELSE deadline_at END
		WHERE source_event_id=? AND task_id=? AND purpose=?
		AND (execution_id='' OR execution_id=?)`, executionID, threadID,
		at.Format(timeFormat), deadline.Format(timeFormat), sourceID,
		taskID, agentTerminalizationPurpose, executionID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("terminalization execution mapping conflicts with the occurrence")
	}
	return nil
}

// ClaimSettledTerminalizations atomically creates one reconciliation execution
// after the original Core execution has settled without a terminal Tasks write.
func (s *taskStore) ClaimSettledTerminalizations(now time.Time, projectID string) ([]*Task, error) {
	now = now.UTC()
	query := `SELECT id FROM tasks WHERE agent_execution_state=? AND agent_settle_deadline_at IS NOT NULL
		AND agent_settle_deadline_at<=? AND state IN ('queued','running')
		AND NOT EXISTS (SELECT 1 FROM task_agent_executions e
			WHERE e.task_id=tasks.id AND e.purpose=?)`
	args := []any{sdk.AgentEventSettled, now.Format(timeFormat), agentTerminalizationPurpose}
	if projectID != "" {
		query += ` AND project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	claimed := []*Task{}
	for _, id := range ids {
		task, changed, claimErr := s.claimTerminalization(id, now)
		if claimErr != nil {
			return nil, claimErr
		}
		if changed {
			claimed = append(claimed, task)
		}
	}
	return claimed, nil
}

func (s *taskStore) claimTerminalization(taskID string, now time.Time) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, taskID))
	if err != nil {
		return nil, false, err
	}
	if terminalState(task.State) || task.AgentExecutionState != sdk.AgentEventSettled ||
		task.AgentSettleDeadline == nil || task.AgentSettleDeadline.After(now) {
		return task, false, nil
	}
	sourceID := taskTerminalizationSourceID(taskID)
	result, err := tx.Exec(`INSERT OR IGNORE INTO task_agent_executions
		(source_event_id, task_id, purpose, thread_id, dispatched_at, updated_at, deadline_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sourceID, taskID, agentTerminalizationPurpose,
		task.AssignedThreadID, now.Format(timeFormat), now.Format(timeFormat),
		nil)
	if err != nil {
		return nil, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return task, false, err
	}
	if _, err = tx.Exec(`UPDATE tasks SET agent_settle_deadline_at=NULL, updated_at=? WHERE id=?`,
		now.Format(timeFormat), taskID); err != nil {
		return nil, false, err
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: taskID, AgentID: task.AgentID,
		EventType: "terminalization_requested", ThreadID: "tasks:lifecycle",
		FromState: task.State, ToState: task.State, Data: map[string]any{
			"source_event_id": sourceID,
		}})
	if err != nil {
		return nil, false, err
	}
	if err := enqueueDeliveryTx(tx, task, task.AssignedThreadID, "task.terminalization_required", "terminalization:"+task.ID, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	updated, err := s.Get(taskID)
	if err == nil {
		s.emit(event)
	}
	return updated, true, err
}

func (s *taskStore) ApplyTerminalizationLifecycle(deliveryID, taskID, projectID string, agentID int64, lifecycle *sdk.AgentEventLifecycle, processedAt time.Time) (*Task, bool, error) {
	if lifecycle == nil || strings.TrimSpace(deliveryID) == "" || lifecycle.SourceEventID != taskTerminalizationSourceID(taskID) {
		return nil, false, errors.New("terminalization lifecycle delivery is incomplete")
	}
	processedAt = processedAt.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	insert, err := tx.Exec(`INSERT OR IGNORE INTO agent_event_lifecycle_deliveries
		(delivery_id, task_id, execution_id, lifecycle_type, sequence, processed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, deliveryID, taskID, lifecycle.ExecutionID,
		lifecycle.Type, lifecycle.Sequence, processedAt.Format(timeFormat))
	if err != nil {
		return nil, false, err
	}
	inserted, err := insert.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if inserted == 0 {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		task, getErr := s.Get(taskID)
		return task, false, getErr
	}
	task, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, taskID))
	if err != nil {
		return nil, false, err
	}
	if task.AgentID != agentID || task.ProjectID != projectID {
		return nil, false, errors.New("terminalization lifecycle is outside the occurrence agent and project")
	}
	execution, err := scanTaskAgentExecution(tx.QueryRow(`SELECT source_event_id, task_id, purpose,
		execution_id, thread_id, state, reason, sequence, dispatched_at, updated_at, deadline_at
		FROM task_agent_executions WHERE source_event_id=?`, lifecycle.SourceEventID))
	if err != nil {
		return nil, false, err
	}
	if execution.ExecutionID != "" && execution.ExecutionID != lifecycle.ExecutionID {
		return nil, false, errors.New("terminalization has a different Core execution")
	}
	if lifecycle.Sequence <= execution.Sequence {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		current, getErr := s.Get(taskID)
		return current, false, getErr
	}
	eventAt := lifecycle.Timestamp.UTC()
	if lifecycle.Timestamp.IsZero() {
		eventAt = processedAt
	}
	var deadline any
	switch lifecycle.Type {
	case sdk.AgentEventClaimed:
		deadline = eventAt.Add(agentSettleGrace).Format(timeFormat)
	case sdk.AgentEventActive:
		deadline = nil
	case sdk.AgentEventSettled:
		deadline = eventAt.Add(agentSettleGrace).Format(timeFormat)
	case sdk.AgentEventError:
		deadline = nil
	default:
		return nil, false, fmt.Errorf("unsupported terminalization lifecycle type %q", lifecycle.Type)
	}
	_, err = tx.Exec(`UPDATE task_agent_executions SET execution_id=?, thread_id=?, state=?, reason=?,
		sequence=?, updated_at=?, deadline_at=? WHERE source_event_id=?`, lifecycle.ExecutionID,
		lifecycle.ThreadID, lifecycle.Type, lifecycle.Reason, lifecycle.Sequence,
		eventAt.Format(timeFormat), deadline, lifecycle.SourceEventID)
	if err != nil {
		return nil, false, err
	}
	fromState, toState := task.State, task.State
	eventType := "terminalization_execution_" + strings.TrimPrefix(lifecycle.Type, "event.")
	if lifecycle.Type == sdk.AgentEventError && !terminalState(task.State) {
		toState = stateFailed
		message := "terminalization_execution_error"
		if reason := strings.TrimSpace(lifecycle.Reason); reason != "" {
			message += ": " + reason
		}
		if err := failOccurrenceTx(tx, task, message, "Terminalization execution failed", processedAt); err != nil {
			return nil, false, err
		}
		eventType = "state_changed"
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: taskID, AgentID: task.AgentID,
		EventType: eventType, ThreadID: lifecycle.ThreadID, FromState: fromState, ToState: toState,
		Data: map[string]any{"delivery_id": deliveryID, "source_event_id": lifecycle.SourceEventID,
			"agent_execution_id": lifecycle.ExecutionID, "lifecycle_type": lifecycle.Type,
			"sequence": lifecycle.Sequence, "reason": lifecycle.Reason, "deadline_at": deadline}})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	updated, err := s.Get(taskID)
	if err == nil {
		s.emit(event)
	}
	return updated, true, err
}

func failOccurrenceTx(tx *sql.Tx, task *Task, message, step string, now time.Time) error {
	if _, err := tx.Exec(`UPDATE tasks SET state='failed', error=?, current_step=?,
		agent_settle_deadline_at=NULL, updated_at=?, completed_at=? WHERE id=? AND state IN ('queued','running','waiting','blocked')`,
		message, step, now.Format(timeFormat), now.Format(timeFormat), task.ID); err != nil {
		return err
	}
	failedTask := *task
	failedTask.State = stateFailed
	failedTask.Error = message
	if err := enqueueTerminalTx(tx, &failedTask, true, "execution_failed", now); err != nil {
		return err
	}
	if task.ParentTaskID != "" {
		return rollupOccurrenceTx(tx, task.ParentTaskID, task.ID, stateFailed, message,
			task.ResultReference, task.DispatchedAt, task.AcceptedAt, now)
	}
	if task.ScheduledFor != nil && task.DispatchedAt != nil {
		_, err := tx.Exec(`UPDATE tasks SET last_occurrence_status='failed', last_error=?,
			last_result_reference=? WHERE id=?`, message, task.ResultReference, task.ID)
		return err
	}
	return nil
}

func (s *taskStore) FailExpiredTerminalizations(now time.Time, projectID string) ([]*Task, error) {
	query := `SELECT e.task_id FROM task_agent_executions e JOIN tasks t ON t.id=e.task_id
		WHERE e.purpose=? AND e.deadline_at IS NOT NULL AND e.deadline_at<=?
		AND t.state IN ('queued','running')`
	args := []any{agentTerminalizationPurpose, now.UTC().Format(timeFormat)}
	if projectID != "" {
		query += ` AND t.project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	failed := []*Task{}
	for _, id := range ids {
		task, changed, failErr := s.failExpiredTerminalization(id, now.UTC())
		if failErr != nil {
			return nil, failErr
		}
		if changed {
			failed = append(failed, task)
		}
	}
	return failed, nil
}

func (s *taskStore) failExpiredTerminalization(taskID string, now time.Time) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, taskID))
	if err != nil {
		return nil, false, err
	}
	execution, err := scanTaskAgentExecution(tx.QueryRow(`SELECT source_event_id, task_id, purpose,
		execution_id, thread_id, state, reason, sequence, dispatched_at, updated_at, deadline_at
		FROM task_agent_executions WHERE source_event_id=?`, taskTerminalizationSourceID(taskID)))
	if err != nil {
		return nil, false, err
	}
	if terminalState(task.State) || execution.DeadlineAt == nil || execution.DeadlineAt.After(now) {
		return task, false, nil
	}
	message := "agent_exited_without_terminal_status: reconciliation wake ended without a terminal Tasks write"
	if execution.State == "" {
		message = "terminalization_wake_unaccepted: reconciliation wake was not accepted"
	}
	if err := failOccurrenceTx(tx, task, message, "Agent execution ended without terminal status", now); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`UPDATE task_agent_executions SET deadline_at=NULL, updated_at=? WHERE source_event_id=?`,
		now.Format(timeFormat), execution.SourceEventID); err != nil {
		return nil, false, err
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: taskID, AgentID: task.AgentID,
		EventType: "state_changed", ThreadID: "tasks:lifecycle", FromState: task.State, ToState: stateFailed,
		Data: map[string]any{"error": message, "terminalization_execution_id": execution.ExecutionID,
			"terminalization_state": execution.State}})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	updated, err := s.Get(taskID)
	if err == nil {
		s.emit(event)
	}
	return updated, true, err
}
