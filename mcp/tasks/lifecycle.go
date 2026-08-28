package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	agentEventSourcePrefix = "occurrence:"
	agentSettleGrace       = 10 * time.Minute
)

func taskAgentEventSourceID(taskID string) string {
	return agentEventSourcePrefix + taskID
}

func taskIDFromAgentEventSource(sourceID string) (string, error) {
	taskID := strings.TrimSpace(strings.TrimPrefix(sourceID, agentEventSourcePrefix))
	if taskID == "" || taskAgentEventSourceID(taskID) != sourceID {
		return "", errors.New("agent lifecycle source_event_id is not a Tasks occurrence")
	}
	return taskID, nil
}

func (a *App) handleAgentEventLifecycle(ctx *sdk.AppCtx, event sdk.Event) error {
	lifecycle, err := sdk.DecodeAgentEventLifecycle(event)
	if err != nil {
		return err
	}
	taskID, err := taskIDFromAgentEventSource(lifecycle.SourceEventID)
	if err != nil {
		return err
	}
	projectID := strings.TrimSpace(event.ProjectID)
	if ctx != nil && strings.TrimSpace(ctx.CurrentProject()) != "" {
		projectID = strings.TrimSpace(ctx.CurrentProject())
	}
	task, changed, err := a.store.ApplyAgentEventLifecycle(event.DeliveryID, taskID, projectID,
		event.InstanceID, lifecycle, nowUTC())
	if err != nil {
		return err
	}
	if changed && task != nil && task.State == stateFailed && lifecycle.Type == sdk.AgentEventError {
		_ = a.notifyExecutionFailure(task, "agent_execution_error")
	}
	return nil
}

// RecordAgentEventExecution links the stable Tasks occurrence to the generic
// Core execution returned by the platform. A lifecycle delivery may win this
// race; both paths accept the same immutable mapping.
func (s *taskStore) RecordAgentEventExecution(taskID, sourceID, executionID string, at time.Time) error {
	if strings.TrimSpace(taskID) == "" || sourceID != taskAgentEventSourceID(taskID) || strings.TrimSpace(executionID) == "" {
		return errors.New("invalid tracked agent execution mapping")
	}
	at = at.UTC()
	result, err := s.db.Exec(`UPDATE tasks SET
		agent_event_source_id=CASE WHEN agent_event_source_id='' THEN ? ELSE agent_event_source_id END,
		agent_execution_id=CASE WHEN agent_execution_id='' THEN ? ELSE agent_execution_id END,
		agent_execution_updated_at=COALESCE(agent_execution_updated_at, ?), updated_at=?
		WHERE id=? AND (agent_event_source_id='' OR agent_event_source_id=?)
		AND (agent_execution_id='' OR agent_execution_id=?)`, sourceID, executionID,
		at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), taskID, sourceID, executionID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("tracked agent execution mapping conflicts with the occurrence")
	}
	return nil
}

// ApplyAgentEventLifecycle transactionally deduplicates one Server delivery
// and projects generic Core execution facts into Tasks-owned occurrence state.
// MCP remains authoritative for progress and terminal business outcomes.
func (s *taskStore) ApplyAgentEventLifecycle(deliveryID, taskID, projectID string, agentID int64, lifecycle *sdk.AgentEventLifecycle, processedAt time.Time) (*Task, bool, error) {
	if lifecycle == nil || strings.TrimSpace(deliveryID) == "" {
		return nil, false, errors.New("agent lifecycle delivery is incomplete")
	}
	if lifecycle.SourceEventID != taskAgentEventSourceID(taskID) {
		return nil, false, errors.New("agent lifecycle source does not match occurrence")
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
		lifecycle.Type, lifecycle.Sequence, processedAt.Format(time.RFC3339Nano))
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

	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, taskID))
	if err != nil {
		return nil, false, err
	}
	if current.AgentID != agentID || current.ProjectID != projectID {
		return nil, false, errors.New("agent lifecycle delivery is outside the occurrence agent and project")
	}
	if current.ScheduledFor == nil || current.DispatchedAt == nil {
		return nil, false, errors.New("agent lifecycle delivery targets an undispatched task")
	}
	if current.AgentEventSourceID != "" && current.AgentEventSourceID != lifecycle.SourceEventID {
		return nil, false, errors.New("occurrence has a different tracked source event")
	}
	if current.AgentExecutionID != "" && current.AgentExecutionID != lifecycle.ExecutionID {
		return nil, false, errors.New("occurrence has a different Core execution")
	}
	if lifecycle.Sequence <= current.AgentLifecycleSequence {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		task, getErr := s.Get(taskID)
		return task, false, getErr
	}

	eventAt := lifecycle.Timestamp.UTC()
	if lifecycle.Timestamp.IsZero() {
		eventAt = processedAt
	}
	state := current.State
	step := current.CurrentStep
	failure := current.Error
	acceptedAt := current.AcceptedAt
	executionThread := current.ExecutionThreadID
	startedAt := current.StartedAt
	completedAt := current.CompletedAt
	telemetryReference := current.TelemetryReference
	var settleDeadline *time.Time
	if current.AgentSettleDeadline != nil {
		v := current.AgentSettleDeadline.UTC()
		settleDeadline = &v
	}

	firstAcceptance := acceptedAt == nil && !terminalState(state)
	if firstAcceptance {
		v := eventAt
		acceptedAt = &v
		executionThread = lifecycle.ThreadID
		telemetryReference = taskTelemetryReference(current.AgentID, lifecycle.ThreadID, *current.DispatchedAt)
	}

	switch lifecycle.Type {
	case sdk.AgentEventClaimed:
		// Claiming proves receipt but not yet business progress.
	case sdk.AgentEventActive:
		if !terminalState(state) {
			settleDeadline = nil
			if state == stateQueued {
				state = stateRunning
				if startedAt == nil {
					v := eventAt
					startedAt = &v
				}
				if step == "" || step == "Scheduled work is ready" {
					step = "Agent processing task"
				}
			}
		}
	case sdk.AgentEventSettled:
		if state == stateQueued || state == stateRunning {
			deadline := eventAt.Add(agentSettleGrace)
			settleDeadline = &deadline
		}
	case sdk.AgentEventError:
		settleDeadline = nil
		if !terminalState(state) {
			state = stateFailed
			failure = "agent_execution_error"
			if reason := strings.TrimSpace(lifecycle.Reason); reason != "" {
				failure += ": " + reason
			}
			step = "Agent execution ended with an error"
			v := processedAt
			completedAt = &v
		}
	default:
		return nil, false, fmt.Errorf("unsupported agent lifecycle type %q", lifecycle.Type)
	}

	var acceptedValue, startedValue, completedValue, settleValue any
	if acceptedAt != nil {
		acceptedValue = acceptedAt.UTC().Format(time.RFC3339Nano)
	}
	if startedAt != nil {
		startedValue = startedAt.UTC().Format(time.RFC3339Nano)
	}
	if completedAt != nil {
		completedValue = completedAt.UTC().Format(time.RFC3339Nano)
	}
	if settleDeadline != nil {
		settleValue = settleDeadline.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.Exec(`UPDATE tasks SET state=?, current_step=?, error=?, accepted_at=?,
		execution_thread_id=?, telemetry_reference=?, agent_event_source_id=?, agent_execution_id=?,
		agent_execution_state=?, agent_execution_updated_at=?, agent_execution_reason=?,
		agent_settle_deadline_at=?, agent_lifecycle_sequence=?, updated_at=?, started_at=?, completed_at=?
		WHERE id=?`, state, step, failure, acceptedValue, executionThread, telemetryReference,
		lifecycle.SourceEventID, lifecycle.ExecutionID, lifecycle.Type, eventAt.Format(time.RFC3339Nano),
		lifecycle.Reason, settleValue, lifecycle.Sequence, processedAt.Format(time.RFC3339Nano),
		startedValue, completedValue, taskID)
	if err != nil {
		return nil, false, err
	}
	if current.ParentTaskID != "" {
		status := occurrenceStatusValues(state, acceptedAt, current.DispatchedAt)
		if err := rollupOccurrenceTx(tx, current.ParentTaskID, current.ID, status, failure,
			current.ResultReference, current.DispatchedAt, acceptedAt, processedAt); err != nil {
			return nil, false, err
		}
	}

	eventType := strings.TrimPrefix(lifecycle.Type, "event.")
	eventType = "agent_execution_" + eventType
	if firstAcceptance && lifecycle.Type == sdk.AgentEventClaimed {
		eventType = "occurrence_accepted"
	} else if state != current.State {
		eventType = "state_changed"
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: taskID, AgentID: current.AgentID,
		EventType: eventType, ThreadID: lifecycle.ThreadID, FromState: current.State, ToState: state,
		Data: map[string]any{"delivery_id": deliveryID, "source_event_id": lifecycle.SourceEventID,
			"agent_execution_id": lifecycle.ExecutionID, "lifecycle_type": lifecycle.Type,
			"sequence": lifecycle.Sequence, "reason": lifecycle.Reason,
			"accepted_at": acceptedValue, "settle_deadline_at": settleValue}})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	task, err := s.Get(taskID)
	if err == nil {
		s.emit(event)
	}
	return task, true, err
}

// ReconcileSettledExecutions closes occurrences whose causal Core execution
// returned idle while queued/running but whose agent omitted the terminal Tasks
// MCP call. Explicit waiting/blocked states remain durable external waits.
func (s *taskStore) ReconcileSettledExecutions(now time.Time, projectID string) ([]*Task, error) {
	now = now.UTC()
	query := `SELECT id FROM tasks WHERE agent_execution_state=? AND agent_settle_deadline_at IS NOT NULL
		AND agent_settle_deadline_at<=? AND state IN ('queued','running')`
	args := []any{sdk.AgentEventSettled, now.Format(time.RFC3339Nano)}
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
	var failed []*Task
	for _, id := range ids {
		task, changed, err := s.failSettledExecution(id, now)
		if err != nil {
			return nil, err
		}
		if changed {
			failed = append(failed, task)
		}
	}
	return failed, nil
}

func (s *taskStore) failSettledExecution(id string, now time.Time) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if terminalState(current.State) || current.AgentExecutionState != sdk.AgentEventSettled ||
		current.AgentSettleDeadline == nil || current.AgentSettleDeadline.After(now) {
		return current, false, nil
	}
	message := "agent_exited_without_terminal_status: Core returned idle before the agent recorded completion or failure"
	step := "Agent execution ended without terminal status"
	_, err = tx.Exec(`UPDATE tasks SET state='failed', error=?, current_step=?,
		agent_settle_deadline_at=NULL, updated_at=?, completed_at=? WHERE id=?`, message, step,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, false, err
	}
	if current.ParentTaskID != "" {
		if err := rollupOccurrenceTx(tx, current.ParentTaskID, current.ID, stateFailed, message,
			current.ResultReference, current.DispatchedAt, current.AcceptedAt, now); err != nil {
			return nil, false, err
		}
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID,
		EventType: "state_changed", ThreadID: "tasks:lifecycle", FromState: current.State, ToState: stateFailed,
		Data: map[string]any{"error": message, "agent_execution_id": current.AgentExecutionID,
			"lifecycle_type": current.AgentExecutionState, "settled_at": current.AgentExecutionUpdated}})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	task, err := s.Get(id)
	if err == nil {
		s.emit(event)
	}
	return task, true, err
}
