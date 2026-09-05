package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type taskStore struct {
	db         *sql.DB
	onEvent    func(TaskEvent)
	recoveryMu sync.Mutex
	clock      func() time.Time
}

func (s *taskStore) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func newTaskStore(db *sql.DB, onEvent func(TaskEvent)) *taskStore {
	return &taskStore{db: db, onEvent: onEvent}
}

const taskColumns = `id, agent_id, project_id, title, description, state,
	progress, current_step, created_by_thread_id, assigned_thread_id,
	execution_thread_id, parent_task_id, idempotency_key, recovery_of_task_id,
	original_occurrence_key, recovery_attempt, recovery_reason, operation_key, schedule_kind,
	schedule_expression, schedule_timezone, schedule_enabled,
	schedule_overlap_policy, schedule_catchup_policy, next_run_at, last_run_at,
	last_dispatched_at, last_occurrence_id, last_occurrence_status, last_error,
	last_result_reference, scheduled_for, schedule_occurrence_key, dispatched_at,
	dispatch_attempts, last_dispatch_attempt_at, accepted_at, telemetry_reference,
	agent_event_source_id, agent_execution_id, agent_execution_state,
	agent_execution_updated_at, agent_execution_reason, agent_settle_deadline_at,
	agent_lifecycle_sequence, result, result_reference, error, created_at,
	updated_at, started_at, completed_at`

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (*Task, error) {
	var t Task
	var progress sql.NullInt64
	var scheduleEnabled int
	var next, last, lastDispatched, scheduled, dispatched, lastDispatchAttempt, accepted, executionUpdated, settleDeadline, started, completed sql.NullString
	var created, updated string
	err := row.Scan(&t.ID, &t.AgentID, &t.ProjectID, &t.Title, &t.Description, &t.State,
		&progress, &t.CurrentStep, &t.CreatedByThreadID, &t.AssignedThreadID,
		&t.ExecutionThreadID, &t.ParentTaskID, &t.IdempotencyKey, &t.RecoveryOfTaskID,
		&t.OriginalOccurrenceKey, &t.RecoveryAttempt, &t.RecoveryReason, &t.OperationKey, &t.ScheduleKind,
		&t.ScheduleExpression, &t.ScheduleTimezone, &scheduleEnabled,
		&t.ScheduleOverlapPolicy, &t.ScheduleCatchupPolicy, &next, &last, &lastDispatched,
		&t.LastOccurrenceID, &t.LastOccurrenceStatus, &t.LastError, &t.LastResultReference,
		&scheduled, &t.ScheduleOccurrenceKey, &dispatched, &t.DispatchAttempts, &lastDispatchAttempt, &accepted, &t.TelemetryReference,
		&t.AgentEventSourceID, &t.AgentExecutionID, &t.AgentExecutionState, &executionUpdated,
		&t.AgentExecutionReason, &settleDeadline, &t.AgentLifecycleSequence,
		&t.Result, &t.ResultReference, &t.Error, &created, &updated, &started, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	if progress.Valid {
		v := int(progress.Int64)
		t.Progress = &v
	}
	t.ScheduleEnabled = scheduleEnabled != 0
	t.NextRunAt = parseNullableTime(next)
	t.LastRunAt = parseNullableTime(last)
	t.LastDispatchedAt = parseNullableTime(lastDispatched)
	t.ScheduledFor = parseNullableTime(scheduled)
	t.DispatchedAt = parseNullableTime(dispatched)
	t.LastDispatchAttemptAt = parseNullableTime(lastDispatchAttempt)
	t.AcceptedAt = parseNullableTime(accepted)
	t.AgentExecutionUpdated = parseNullableTime(executionUpdated)
	t.AgentSettleDeadline = parseNullableTime(settleDeadline)
	t.StartedAt = parseNullableTime(started)
	t.CompletedAt = parseNullableTime(completed)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &t, nil
}

func parseNullableTime(v sql.NullString) *time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		t, err = time.Parse(time.RFC3339, v.String)
	}
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%x", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

func (s *taskStore) emit(event TaskEvent) {
	if s.onEvent != nil {
		s.onEvent(event)
	}
}

func insertEvent(tx *sql.Tx, event TaskEvent) (TaskEvent, error) {
	if event.ID == "" {
		event.ID = newID("event-")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	data, _ := json.Marshal(event.Data)
	_, err := tx.Exec(`INSERT INTO task_events
		(id, task_id, agent_id, event_type, thread_id, from_state, to_state, data_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.TaskID, event.AgentID,
		event.EventType, event.ThreadID, event.FromState, event.ToState, string(data),
		event.CreatedAt.Format(timeFormat))
	return event, err
}

func (s *taskStore) Create(input CreateTaskInput) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var events []TaskEvent
	task, created, err := s.createTx(tx, input, s.now(), &events)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	for _, event := range events {
		s.emit(event)
	}
	return task, created, nil
}

func (s *taskStore) createTx(tx *sql.Tx, input CreateTaskInput, now time.Time, events *[]TaskEvent) (*Task, bool, error) {
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Title = strings.TrimSpace(input.Title)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.AssignedThreadID = strings.TrimSpace(input.AssignedThreadID)
	input.CreatedByThreadID = strings.TrimSpace(input.CreatedByThreadID)
	if input.AgentID <= 0 || input.ProjectID == "" || input.Title == "" || input.AssignedThreadID == "" {
		return nil, false, validationError("agent_id, project_id, title, and assigned_thread_id are required")
	}
	if input.State == "" {
		input.State = stateQueued
	}
	if !validState(input.State) {
		return nil, false, fmt.Errorf("%w: %s", errInvalidState, input.State)
	}
	if input.Progress != nil && (*input.Progress < 0 || *input.Progress > 100) {
		return nil, false, errInvalidProgress
	}
	if input.IdempotencyKey != "" {
		existing, getErr := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE agent_id=? AND idempotency_key=?`, input.AgentID, input.IdempotencyKey))
		if getErr == nil {
			return existing, false, nil
		}
		if !errors.Is(getErr, errTaskNotFound) {
			return nil, false, getErr
		}
	}
	var normalized *normalizedSchedule
	var err error
	if input.Schedule != nil {
		value, normalizeErr := normalizeSchedule(*input.Schedule, now)
		if normalizeErr != nil {
			return nil, false, normalizeErr
		}
		normalized = &value
		input.State = stateWaiting
		input.Progress = nil
		input.CurrentStep = ""
	}

	id := newID("task-")
	var progress any
	if input.Progress != nil {
		progress = *input.Progress
	}
	var started, completed any
	if input.State == stateRunning {
		started = now.Format(timeFormat)
	}
	if terminalState(input.State) {
		completed = now.Format(timeFormat)
	}
	var scheduleKind, expression, timezone, overlap, catchup string
	var enabled int
	var nextRun any
	if normalized != nil {
		scheduleKind, expression, timezone = normalized.Kind, normalized.Expression, normalized.Timezone
		overlap, catchup, enabled = normalized.OverlapPolicy, normalized.CatchupPolicy, 1
		nextRun = normalized.NextRunAt.Format(timeFormat)
	}
	var scheduledFor any
	if input.ScheduledFor != nil {
		scheduledFor = input.ScheduledFor.UTC().Format(timeFormat)
	}
	_, err = tx.Exec(`INSERT INTO tasks (`+taskColumns+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", strings.Count(taskColumns, ",")+1), ",")+`)`,
		id, input.AgentID, input.ProjectID, input.Title, strings.TrimSpace(input.Description), input.State,
		progress, strings.TrimSpace(input.CurrentStep), input.CreatedByThreadID, input.AssignedThreadID,
		"", strings.TrimSpace(input.ParentTaskID), strings.TrimSpace(input.IdempotencyKey),
		strings.TrimSpace(input.RecoveryOfTaskID), strings.TrimSpace(input.OriginalOccurrenceKey), input.RecoveryAttempt,
		strings.TrimSpace(input.RecoveryReason), strings.TrimSpace(input.OperationKey), scheduleKind,
		expression, timezone, enabled, overlap, catchup, nextRun, nil, nil, "", "", "", "",
		scheduledFor, strings.TrimSpace(input.OccurrenceKey), nil, 0, nil, nil, "",
		"", "", "", nil, "", nil, 0,
		"", "", "",
		now.Format(timeFormat),
		now.Format(timeFormat), started, completed)
	if err != nil {
		return nil, false, err
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: input.AgentID, EventType: "created",
		ThreadID: input.CreatedByThreadID, ToState: input.State, Data: map[string]any{
			"title": input.Title, "assigned_thread_id": input.AssignedThreadID,
			"schedule_kind": scheduleKind, "next_run_at": nextRun,
		}})
	if err != nil {
		return nil, false, err
	}
	task, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if err := enqueueCreatedTaskTx(tx, task, now); err != nil {
		return nil, false, err
	}
	*events = append(*events, event)
	return task, true, nil
}

func (s *taskStore) Get(id string) (*Task, error) {
	return scanTask(s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, strings.TrimSpace(id)))
}

func (s *taskStore) List(filter TaskFilter) ([]Task, error) {
	page, err := s.ListPage(filter)
	return page.Tasks, err
}

func (s *taskStore) Update(id, actorThread string, input UpdateTaskInput) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if ((input.Title != nil && strings.TrimSpace(*input.Title) != current.Title) || (input.Description != nil && *input.Description != current.Description)) &&
		(current.ParentTaskID != "" || current.ScheduledFor != nil) {
		return nil, false, validationError("cannot edit an occurrence definition; update its recurring parent task")
	}
	title := current.Title
	if input.Title != nil {
		title = strings.TrimSpace(*input.Title)
		if title == "" {
			return nil, false, validationError("title cannot be empty")
		}
	}
	description := current.Description
	if input.Description != nil {
		description = *input.Description
	}

	scheduleKind, scheduleExpression, scheduleTimezone := current.ScheduleKind, current.ScheduleExpression, current.ScheduleTimezone
	scheduleOverlap, scheduleCatchup, scheduleEnabled := current.ScheduleOverlapPolicy, current.ScheduleCatchupPolicy, current.ScheduleEnabled
	nextRunAt := current.NextRunAt
	scheduleChanged, addingSchedule := false, false
	if input.Schedule != nil {
		if terminalState(current.State) {
			return nil, false, errTerminalTask
		}
		if current.ParentTaskID != "" || current.ScheduledFor != nil {
			return nil, false, validationError("cannot edit an occurrence schedule; update its recurring parent task")
		}
		n, changed, scheduleErr := mergeSchedulePatch(current, *input.Schedule, time.Now().UTC())
		if scheduleErr != nil {
			return nil, false, scheduleErr
		}
		scheduleChanged = changed
		addingSchedule = current.ScheduleKind == ""
		scheduleKind, scheduleExpression, scheduleTimezone = n.Kind, n.Expression, n.Timezone
		scheduleOverlap, scheduleCatchup = n.OverlapPolicy, n.CatchupPolicy
		if addingSchedule {
			scheduleEnabled = true
		}
		next := n.NextRunAt.UTC()
		nextRunAt = &next
	}
	state := current.State
	if input.State != nil {
		state = strings.ToLower(strings.TrimSpace(*input.State))
		if !validState(state) {
			return nil, false, fmt.Errorf("%w: %s", errInvalidState, state)
		}
		if !validTransition(current.State, state) {
			return nil, false, errTerminalTask
		}
	} else if addingSchedule {
		state = stateWaiting
	}
	progress := current.Progress
	if addingSchedule && input.Progress == nil {
		progress = nil
	}
	if input.ClearProgress {
		progress = nil
	}
	if input.Progress != nil {
		if *input.Progress < 0 || *input.Progress > 100 {
			return nil, false, errInvalidProgress
		}
		if current.Progress != nil && *input.Progress < *current.Progress {
			return nil, false, validationError("progress cannot decrease")
		}
		v := *input.Progress
		progress = &v
	}
	if state == stateCompleted {
		v := 100
		progress = &v
	}
	step, assigned, execution, result, resultReference, failure := current.CurrentStep, current.AssignedThreadID, current.ExecutionThreadID, current.Result, current.ResultReference, current.Error
	if input.CurrentStep != nil {
		step = strings.TrimSpace(*input.CurrentStep)
	} else if addingSchedule {
		step = ""
	} else if state == stateCompleted {
		step = "Completed"
	}
	if input.AssignedThreadID != nil {
		assigned = strings.TrimSpace(*input.AssignedThreadID)
		if assigned != current.AssignedThreadID {
			execution = ""
		}
		if assigned == "" {
			return nil, false, validationError("assigned_thread_id cannot be empty")
		}
	}
	if input.ExecutionThreadID != nil {
		execution = strings.TrimSpace(*input.ExecutionThreadID)
	}
	if input.Result != nil {
		result = strings.TrimSpace(*input.Result)
	}
	if input.ResultReference != nil {
		resultReference = strings.TrimSpace(*input.ResultReference)
	}
	if terminalState(state) && resultReference == "" && result != "" {
		resultReference = "task:" + current.ID
	}
	if input.Error != nil {
		failure = strings.TrimSpace(*input.Error)
	}
	changed := title != current.Title || description != current.Description || scheduleChanged ||
		state != current.State || !equalProgress(progress, current.Progress) || step != current.CurrentStep ||
		assigned != current.AssignedThreadID || execution != current.ExecutionThreadID || result != current.Result ||
		resultReference != current.ResultReference || failure != current.Error
	if terminalState(current.State) && changed {
		return nil, false, errTerminalTask
	}
	if isScheduleDefinitionTask(current) && input.State != nil && !terminalState(state) && state != stateWaiting {
		return nil, false, validationError("update an occurrence's progress, not its schedule definition")
	}
	if state == stateCompleted && result == "" {
		return nil, false, validationError("result required for completion")
	}
	if state == stateFailed && failure == "" {
		return nil, false, validationError("error required for failure")
	}
	if !changed {
		return current, false, nil
	}
	now := s.now()
	var progressValue any
	if progress != nil {
		progressValue = *progress
	}
	var started any
	if current.StartedAt != nil {
		started = current.StartedAt.Format(timeFormat)
	} else if state == stateRunning {
		started = now.Format(timeFormat)
	}
	var completed any
	if terminalState(state) {
		completed = now.Format(timeFormat)
	}
	var settleDeadline any
	if !terminalState(state) && current.AgentSettleDeadline != nil {
		settleDeadline = current.AgentSettleDeadline.Format(timeFormat)
	}
	acceptedAt := current.AcceptedAt
	telemetryReference := current.TelemetryReference
	if acceptedAt == nil && current.DispatchedAt != nil && actorThread == assigned && (state == stateRunning || terminalState(state)) {
		acceptedAt = &now
		if execution == "" {
			execution = actorThread
		}
		telemetryReference = taskTelemetryReference(current.AgentID, actorThread, *current.DispatchedAt)
	}
	var acceptedValue any
	if acceptedAt != nil {
		acceptedValue = acceptedAt.Format(timeFormat)
	}
	var nextRunValue any
	if nextRunAt != nil {
		nextRunValue = nextRunAt.UTC().Format(timeFormat)
	}
	enabledValue := 0
	if terminalState(state) {
		scheduleEnabled = false
		nextRunValue = nil
	}
	if scheduleEnabled {
		enabledValue = 1
	}
	if input.Schedule != nil {
		_, err = tx.Exec(`UPDATE tasks SET title=?, description=?, state=?, progress=?, current_step=?,
			assigned_thread_id=?, execution_thread_id=?, accepted_at=?, telemetry_reference=?, result=?,
			result_reference=?, error=?, schedule_kind=?, schedule_expression=?, schedule_timezone=?,
			schedule_enabled=?, schedule_overlap_policy=?, schedule_catchup_policy=?, next_run_at=?,
			agent_settle_deadline_at=?, updated_at=?, started_at=?, completed_at=? WHERE id=?`, title, description, state,
			progressValue, step, assigned, execution, acceptedValue, telemetryReference, result, resultReference,
			failure, scheduleKind, scheduleExpression, scheduleTimezone, enabledValue, scheduleOverlap,
			scheduleCatchup, nextRunValue, settleDeadline, now.Format(timeFormat), started, completed, id)
	} else {
		_, err = tx.Exec(`UPDATE tasks SET title=?, description=?, state=?, progress=?, current_step=?,
			assigned_thread_id=?, execution_thread_id=?, accepted_at=?, telemetry_reference=?, result=?,
			result_reference=?, error=?, schedule_enabled=?, next_run_at=?, agent_settle_deadline_at=?, updated_at=?, started_at=?, completed_at=?
			WHERE id=?`, title, description, state, progressValue, step, assigned, execution,
			acceptedValue, telemetryReference, result, resultReference, failure, enabledValue, nextRunValue, settleDeadline, now.Format(timeFormat),
			started, completed, id)
	}
	if err != nil {
		return nil, false, err
	}
	if current.ParentTaskID != "" {
		status := occurrenceStatusValues(state, acceptedAt, current.DispatchedAt)
		if err := rollupOccurrenceTx(tx, current.ParentTaskID, current.ID, status, failure, resultReference, current.DispatchedAt, acceptedAt, now); err != nil {
			return nil, false, err
		}
	} else if current.ScheduledFor != nil && current.DispatchedAt != nil {
		status := occurrenceStatusValues(state, acceptedAt, current.DispatchedAt)
		var acceptedRun any
		if acceptedAt != nil {
			acceptedRun = acceptedAt.Format(timeFormat)
		}
		_, err = tx.Exec(`UPDATE tasks SET last_occurrence_status=?, last_error=?, last_result_reference=?,
			last_run_at=COALESCE(?, last_run_at) WHERE id=?`, status, failure, resultReference, acceptedRun, current.ID)
		if err != nil {
			return nil, false, err
		}
	}
	eventType := "updated"
	if scheduleChanged {
		eventType = "schedule_updated"
	} else if state != current.State {
		eventType = "state_changed"
	}
	changedFields := []string{}
	for field, fieldChanged := range map[string]bool{
		"title": title != current.Title, "description": description != current.Description,
		"schedule": scheduleChanged, "state": state != current.State,
		"progress": !equalProgress(progress, current.Progress), "current_step": step != current.CurrentStep,
		"assigned_thread_id":  assigned != current.AssignedThreadID,
		"execution_thread_id": execution != current.ExecutionThreadID,
		"result":              result != current.Result, "result_reference": resultReference != current.ResultReference,
		"error": failure != current.Error,
	} {
		if fieldChanged {
			changedFields = append(changedFields, field)
		}
	}
	sort.Strings(changedFields)
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID, EventType: eventType, ThreadID: actorThread, FromState: current.State, ToState: state, Data: map[string]any{
		"progress": progressValue, "current_step": step, "assigned_thread_id": assigned,
		"execution_thread_id": execution, "title_changed": title != current.Title,
		"description_changed": description != current.Description, "schedule_changed": scheduleChanged,
		"schedule_kind": scheduleKind, "schedule_timezone": scheduleTimezone,
		"next_run_at": nextRunValue, "changed_fields": changedFields, "result_reference": resultReference,
	}})
	if err != nil {
		return nil, false, err
	}
	updatedTask, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if terminalState(state) && !terminalState(current.State) {
		if err := enqueueTerminalTx(tx, updatedTask, false, "", now); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(`UPDATE task_deliveries SET delivered_at=?, last_error='superseded by terminal state' WHERE task_id=? AND event_type IN ('task.assigned','task.ready','task.recovery.ready','task.terminalization_required') AND delivered_at IS NULL`, now.Format(timeFormat), id); err != nil {
			return nil, false, err
		}
	} else if assigned != current.AssignedThreadID && !isScheduleDefinitionTask(updatedTask) && assigned != actorThread {
		if err := enqueueDeliveryTx(tx, updatedTask, assigned, "task.assigned", "assignment:"+event.ID, now); err != nil {
			return nil, false, err
		}
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

func taskTelemetryReference(agentID int64, threadID string, since time.Time) string {
	return fmt.Sprintf("/api/telemetry?agent_id=%d&thread_id=%s&since=%s", agentID,
		url.QueryEscape(threadID), url.QueryEscape(since.UTC().Format(timeFormat)))
}

func occurrenceStatusValues(state string, acceptedAt, dispatchedAt *time.Time) string {
	if state == stateQueued && acceptedAt != nil {
		return "accepted"
	}
	if state == stateQueued && dispatchedAt != nil {
		return "dispatched"
	}
	return state
}

func rollupOccurrenceTx(tx *sql.Tx, parentID, occurrenceID, status, failure, resultReference string, dispatchedAt, acceptedAt *time.Time, now time.Time) error {
	var dispatchedValue, acceptedValue any
	if dispatchedAt != nil {
		dispatchedValue = dispatchedAt.UTC().Format(timeFormat)
	}
	if acceptedAt != nil {
		acceptedValue = acceptedAt.UTC().Format(timeFormat)
	}
	_, err := tx.Exec(`UPDATE tasks SET
		last_occurrence_status=?, last_error=?, last_result_reference=?,
		last_dispatched_at=COALESCE(?, last_dispatched_at),
		last_run_at=CASE WHEN ? IS NOT NULL AND (last_run_at IS NULL OR last_run_at < ?) THEN ? ELSE last_run_at END,
		updated_at=?
		WHERE id=? AND last_occurrence_id=?`, status, failure, resultReference,
		dispatchedValue, acceptedValue, acceptedValue, acceptedValue,
		now.UTC().Format(timeFormat), parentID, occurrenceID)
	return err
}

// MarkDispatched records the scheduler-to-Core handoff separately from agent
// acceptance. A successful scheduler tick therefore never masquerades as a
// workflow run.
func (s *taskStore) MarkDispatched(id, actor string, at time.Time) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if current.DispatchedAt != nil || terminalState(current.State) {
		return current, false, nil
	}
	at = at.UTC()
	telemetryReference := taskTelemetryReference(current.AgentID, current.AssignedThreadID, at)
	sourceEventID := taskAgentEventSourceID(current.ID)
	_, err = tx.Exec(`UPDATE tasks SET dispatched_at=?, dispatch_attempts=1,
		last_dispatch_attempt_at=?, telemetry_reference=?, agent_event_source_id=?, updated_at=? WHERE id=?`,
		at.Format(timeFormat), at.Format(timeFormat), telemetryReference,
		sourceEventID, at.Format(timeFormat), id)
	if err != nil {
		return nil, false, err
	}
	if current.ParentTaskID != "" {
		_, err = tx.Exec(`UPDATE tasks SET last_dispatched_at=?, last_occurrence_id=?,
			last_occurrence_status='dispatched', last_error='', last_result_reference='', updated_at=? WHERE id=?`,
			at.Format(timeFormat), current.ID, at.Format(timeFormat), current.ParentTaskID)
	} else if current.ScheduledFor != nil {
		_, err = tx.Exec(`UPDATE tasks SET last_dispatched_at=?, last_occurrence_id=?,
			last_occurrence_status='dispatched', last_error='', last_result_reference='' WHERE id=?`,
			at.Format(timeFormat), current.ID, current.ID)
	}
	if err != nil {
		return nil, false, err
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID,
		EventType: "occurrence_dispatched", ThreadID: actor, FromState: current.State,
		ToState: current.State, Data: map[string]any{"dispatched_at": at, "dispatch_attempt": 1,
			"telemetry_reference": telemetryReference, "source_event_id": sourceEventID}})
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

const (
	dispatchReconcileNone  = ""
	dispatchReconcileRetry = "retry"
	dispatchReconcileFail  = "fail"
)

// ReconcileUnacceptedDispatch atomically claims the next retry or terminally
// fails an occurrence whose assigned thread has not accepted it. The atomic
// claim prevents repeated or concurrent scheduler ticks from emitting the same
// retry more than once.
func (s *taskStore) ReconcileUnacceptedDispatch(id, actor string, at, eligibleBefore time.Time, maxAttempts int) (*Task, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, dispatchReconcileNone, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, dispatchReconcileNone, err
	}
	if current.State != stateQueued || current.ScheduledFor == nil || current.DispatchedAt == nil || current.AcceptedAt != nil {
		return current, dispatchReconcileNone, nil
	}
	lastAttempt := current.LastDispatchAttemptAt
	if lastAttempt == nil {
		lastAttempt = current.DispatchedAt
	}
	if lastAttempt == nil || lastAttempt.After(eligibleBefore) {
		return current, dispatchReconcileNone, nil
	}
	at = at.UTC()
	attempts := current.DispatchAttempts
	if attempts < 1 {
		// Rows dispatched before migration 006 already received one task.ready.
		attempts = 1
	}

	var event TaskEvent
	action := dispatchReconcileRetry
	if attempts < maxAttempts {
		attempts++
		result, updateErr := tx.Exec(`UPDATE tasks SET dispatch_attempts=?, last_dispatch_attempt_at=?, updated_at=?
			WHERE id=? AND state='queued' AND accepted_at IS NULL
			AND COALESCE(last_dispatch_attempt_at, dispatched_at)<=?`, attempts,
			at.Format(timeFormat), at.Format(timeFormat), id, eligibleBefore.UTC().Format(timeFormat))
		if updateErr != nil {
			return nil, dispatchReconcileNone, updateErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, dispatchReconcileNone, rowsErr
		}
		if changed == 0 {
			return current, dispatchReconcileNone, nil
		}
		event, err = insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID,
			EventType: "occurrence_redispatched", ThreadID: actor, FromState: current.State,
			ToState: current.State, Data: map[string]any{"dispatch_attempt": attempts, "max_dispatch_attempts": maxAttempts,
				"first_dispatched_at": current.DispatchedAt, "last_dispatch_attempt_at": at}})
	} else {
		action = dispatchReconcileFail
		message := fmt.Sprintf("dispatched but not accepted after %d delivery attempts", attempts)
		result, updateErr := tx.Exec(`UPDATE tasks SET state='failed', error=?, updated_at=?, completed_at=?
			WHERE id=? AND state='queued' AND accepted_at IS NULL
			AND COALESCE(last_dispatch_attempt_at, dispatched_at)<=?`, message,
			at.Format(timeFormat), at.Format(timeFormat), id,
			eligibleBefore.UTC().Format(timeFormat))
		if updateErr != nil {
			return nil, dispatchReconcileNone, updateErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, dispatchReconcileNone, rowsErr
		}
		if changed == 0 {
			return current, dispatchReconcileNone, nil
		}
		if current.ParentTaskID != "" {
			if err := rollupOccurrenceTx(tx, current.ParentTaskID, current.ID, stateFailed, message, current.ResultReference,
				current.DispatchedAt, current.AcceptedAt, at); err != nil {
				return nil, dispatchReconcileNone, err
			}
		} else {
			_, err = tx.Exec(`UPDATE tasks SET last_occurrence_status='failed', last_error=? WHERE id=?`, message, current.ID)
			if err != nil {
				return nil, dispatchReconcileNone, err
			}
		}
		failedTask := *current
		failedTask.State = stateFailed
		failedTask.Error = message
		if err := enqueueTerminalTx(tx, &failedTask, true, "dispatch_unaccepted", at); err != nil {
			return nil, dispatchReconcileNone, err
		}
		event, err = insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID,
			EventType: "state_changed", ThreadID: actor, FromState: current.State, ToState: stateFailed,
			Data: map[string]any{"error": message, "dispatch_attempts": attempts, "current_step": current.CurrentStep}})
	}
	if err != nil {
		return nil, dispatchReconcileNone, err
	}
	if action == dispatchReconcileRetry {
		if _, err := tx.Exec(`UPDATE task_deliveries SET delivered_at=NULL,next_attempt_at=? WHERE id=?`, at.Format(timeFormat), "execution:"+id); err != nil {
			return nil, dispatchReconcileNone, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, dispatchReconcileNone, err
	}
	task, err := s.Get(id)
	if err == nil {
		s.emit(event)
	}
	return task, action, err
}

// Accept records the first authoritative read by the assigned execution
// thread. Merely listing a task or dispatching a wake does not count as a run.
func (s *taskStore) Accept(id, actorThread string, at time.Time) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if current.DispatchedAt == nil || current.AcceptedAt != nil || terminalState(current.State) || strings.TrimSpace(actorThread) != current.AssignedThreadID {
		return current, false, nil
	}
	at = at.UTC()
	telemetryReference := taskTelemetryReference(current.AgentID, actorThread, *current.DispatchedAt)
	_, err = tx.Exec(`UPDATE tasks SET accepted_at=?, execution_thread_id=?, telemetry_reference=?, updated_at=? WHERE id=?`,
		at.Format(timeFormat), actorThread, telemetryReference, at.Format(timeFormat), id)
	if err != nil {
		return nil, false, err
	}
	if current.ParentTaskID != "" {
		acceptedAt := at
		if err := rollupOccurrenceTx(tx, current.ParentTaskID, current.ID, "accepted", "", "", current.DispatchedAt, &acceptedAt, at); err != nil {
			return nil, false, err
		}
	} else if current.ScheduledFor != nil {
		_, err = tx.Exec(`UPDATE tasks SET last_run_at=?, last_occurrence_status='accepted' WHERE id=?`, at.Format(timeFormat), current.ID)
		if err != nil {
			return nil, false, err
		}
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID,
		EventType: "occurrence_accepted", ThreadID: actorThread, FromState: current.State,
		ToState: current.State, Data: map[string]any{"accepted_at": at, "execution_thread_id": actorThread, "telemetry_reference": telemetryReference}})
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

func equalProgress(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *taskStore) Events(taskID string) ([]TaskEvent, error) {
	page, err := s.EventsPage(taskID, "", 100)
	return page.Events, err
}

func (s *taskStore) Counts(projectID string, agentID int64, includeRuns bool) (TaskCounts, error) {
	var out TaskCounts
	where := ` WHERE project_id=?`
	args := []any{projectID}
	if agentID > 0 {
		where += ` AND agent_id=?`
		args = append(args, agentID)
	}
	if !includeRuns {
		where += ` AND parent_task_id=''`
	}
	sum := func(condition string) string { return `COALESCE(SUM(CASE WHEN ` + condition + ` THEN 1 ELSE 0 END),0)` }
	columns := []string{sum(`state IN ('queued','running','waiting','blocked') AND NOT ` + definitionPredicate)}
	for _, state := range []string{stateQueued, stateRunning, stateWaiting, stateBlocked, stateCompleted, stateFailed, stateCancelled} {
		columns = append(columns, sum("state='"+state+"'"))
	}
	columns = append(columns, sum(definitionPredicate+` AND state='waiting' AND schedule_enabled=1`), sum(definitionPredicate+` AND state='waiting' AND schedule_enabled=0`))
	err := s.db.QueryRow(`SELECT `+strings.Join(columns, ",")+` FROM tasks`+where, args...).Scan(&out.Active, &out.Queued, &out.Running, &out.Waiting, &out.Blocked, &out.Completed, &out.Failed, &out.Cancelled, &out.Scheduled, &out.Paused)
	return out, err
}
