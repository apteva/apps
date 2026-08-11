package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type taskStore struct {
	db      *sql.DB
	onEvent func(TaskEvent)
}

func newTaskStore(db *sql.DB, onEvent func(TaskEvent)) *taskStore {
	return &taskStore{db: db, onEvent: onEvent}
}

const taskColumns = `id, agent_id, project_id, title, description, state,
	progress, current_step, created_by_thread_id, assigned_thread_id,
	execution_thread_id, parent_task_id, idempotency_key, schedule_kind,
	schedule_expression, schedule_timezone, schedule_enabled,
	schedule_overlap_policy, schedule_catchup_policy, next_run_at, last_run_at,
	scheduled_for, schedule_occurrence_key, result, error, created_at, updated_at,
	started_at, completed_at`

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (*Task, error) {
	var t Task
	var progress sql.NullInt64
	var scheduleEnabled int
	var next, last, scheduled, started, completed sql.NullString
	var created, updated string
	err := row.Scan(&t.ID, &t.AgentID, &t.ProjectID, &t.Title, &t.Description, &t.State,
		&progress, &t.CurrentStep, &t.CreatedByThreadID, &t.AssignedThreadID,
		&t.ExecutionThreadID, &t.ParentTaskID, &t.IdempotencyKey, &t.ScheduleKind,
		&t.ScheduleExpression, &t.ScheduleTimezone, &scheduleEnabled,
		&t.ScheduleOverlapPolicy, &t.ScheduleCatchupPolicy, &next, &last, &scheduled,
		&t.ScheduleOccurrenceKey, &t.Result, &t.Error, &created, &updated, &started, &completed)
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
	t.ScheduledFor = parseNullableTime(scheduled)
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
		event.CreatedAt.Format(time.RFC3339Nano))
	return event, err
}

func (s *taskStore) Create(input CreateTaskInput) (*Task, bool, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.AssignedThreadID = strings.TrimSpace(input.AssignedThreadID)
	input.CreatedByThreadID = strings.TrimSpace(input.CreatedByThreadID)
	if input.AgentID <= 0 || input.ProjectID == "" || input.Title == "" || input.AssignedThreadID == "" {
		return nil, false, errors.New("agent_id, project_id, title, and assigned_thread_id are required")
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
	var normalized *normalizedSchedule
	var err error
	if input.Schedule != nil {
		value, normalizeErr := normalizeSchedule(*input.Schedule, time.Now().UTC())
		if normalizeErr != nil {
			return nil, false, normalizeErr
		}
		normalized = &value
		input.State = stateWaiting
		input.Progress = nil
		input.CurrentStep = ""
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if input.IdempotencyKey != "" {
		existing, getErr := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE agent_id=? AND idempotency_key=?`, input.AgentID, input.IdempotencyKey))
		if getErr == nil {
			return existing, false, nil
		}
		if !errors.Is(getErr, errTaskNotFound) {
			return nil, false, getErr
		}
	}
	now := time.Now().UTC()
	id := newID("task-")
	var progress any
	if input.Progress != nil {
		progress = *input.Progress
	}
	var started, completed any
	if input.State == stateRunning {
		started = now.Format(time.RFC3339Nano)
	}
	if terminalState(input.State) {
		completed = now.Format(time.RFC3339Nano)
	}
	var scheduleKind, expression, timezone, overlap, catchup string
	var enabled int
	var nextRun any
	if normalized != nil {
		scheduleKind, expression, timezone = normalized.Kind, normalized.Expression, normalized.Timezone
		overlap, catchup, enabled = normalized.OverlapPolicy, normalized.CatchupPolicy, 1
		nextRun = normalized.NextRunAt.Format(time.RFC3339Nano)
	}
	var scheduledFor any
	if input.ScheduledFor != nil {
		scheduledFor = input.ScheduledFor.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.Exec(`INSERT INTO tasks (`+taskColumns+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", 29), ",")+`)`,
		id, input.AgentID, input.ProjectID, input.Title, strings.TrimSpace(input.Description), input.State,
		progress, strings.TrimSpace(input.CurrentStep), input.CreatedByThreadID, input.AssignedThreadID,
		"", strings.TrimSpace(input.ParentTaskID), strings.TrimSpace(input.IdempotencyKey), scheduleKind,
		expression, timezone, enabled, overlap, catchup, nextRun, nil, scheduledFor,
		strings.TrimSpace(input.OccurrenceKey), "", "", now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), started, completed)
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
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	task, err := s.Get(id)
	if err == nil {
		s.emit(event)
	}
	return task, true, err
}

func (s *taskStore) Get(id string) (*Task, error) {
	return scanTask(s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, strings.TrimSpace(id)))
}

func (s *taskStore) List(filter TaskFilter) ([]Task, error) {
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE 1=1`
	args := []any{}
	if filter.ProjectID != "" {
		query += ` AND project_id=?`
		args = append(args, filter.ProjectID)
	}
	if filter.AgentID > 0 {
		query += ` AND agent_id=?`
		args = append(args, filter.AgentID)
	}
	if len(filter.States) > 0 {
		marks := make([]string, len(filter.States))
		for i, state := range filter.States {
			if !validState(state) {
				return nil, fmt.Errorf("%w: %s", errInvalidState, state)
			}
			marks[i] = "?"
			args = append(args, state)
		}
		query += ` AND state IN (` + strings.Join(marks, ",") + `)`
	}
	if filter.ParentTaskID != nil {
		query += ` AND parent_task_id=?`
		args = append(args, *filter.ParentTaskID)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		task, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *task)
	}
	return out, rows.Err()
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
	state := current.State
	if input.State != nil {
		state = strings.ToLower(strings.TrimSpace(*input.State))
		if !validState(state) {
			return nil, false, fmt.Errorf("%w: %s", errInvalidState, state)
		}
		if !validTransition(current.State, state) {
			return nil, false, errTerminalTask
		}
	}
	progress := current.Progress
	if input.ClearProgress {
		progress = nil
	}
	if input.Progress != nil {
		if *input.Progress < 0 || *input.Progress > 100 {
			return nil, false, errInvalidProgress
		}
		v := *input.Progress
		progress = &v
	}
	if state == stateCompleted {
		v := 100
		progress = &v
	}
	step, assigned, execution, result, failure := current.CurrentStep, current.AssignedThreadID, current.ExecutionThreadID, current.Result, current.Error
	if input.CurrentStep != nil {
		step = strings.TrimSpace(*input.CurrentStep)
	} else if state == stateCompleted {
		step = "Completed"
	}
	if input.AssignedThreadID != nil {
		assigned = strings.TrimSpace(*input.AssignedThreadID)
		if assigned == "" {
			return nil, false, errors.New("assigned_thread_id cannot be empty")
		}
	}
	if input.ExecutionThreadID != nil {
		execution = strings.TrimSpace(*input.ExecutionThreadID)
	}
	if input.Result != nil {
		result = strings.TrimSpace(*input.Result)
	}
	if input.Error != nil {
		failure = strings.TrimSpace(*input.Error)
	}
	changed := state != current.State || !equalProgress(progress, current.Progress) || step != current.CurrentStep || assigned != current.AssignedThreadID || execution != current.ExecutionThreadID || result != current.Result || failure != current.Error
	if !changed {
		return current, false, nil
	}
	now := time.Now().UTC()
	var progressValue any
	if progress != nil {
		progressValue = *progress
	}
	var started any
	if current.StartedAt != nil {
		started = current.StartedAt.Format(time.RFC3339Nano)
	} else if state == stateRunning {
		started = now.Format(time.RFC3339Nano)
	}
	var completed any
	if terminalState(state) {
		completed = now.Format(time.RFC3339Nano)
	}
	_, err = tx.Exec(`UPDATE tasks SET state=?, progress=?, current_step=?, assigned_thread_id=?, execution_thread_id=?, result=?, error=?, updated_at=?, started_at=?, completed_at=? WHERE id=?`, state, progressValue, step, assigned, execution, result, failure, now.Format(time.RFC3339Nano), started, completed, id)
	if err != nil {
		return nil, false, err
	}
	eventType := "updated"
	if state != current.State {
		eventType = "state_changed"
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: current.AgentID, EventType: eventType, ThreadID: actorThread, FromState: current.State, ToState: state, Data: map[string]any{"progress": progressValue, "current_step": step, "assigned_thread_id": assigned, "execution_thread_id": execution}})
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
	rows, err := s.db.Query(`SELECT id, task_id, agent_id, event_type, thread_id, from_state, to_state, data_json, created_at FROM task_events WHERE task_id=? ORDER BY created_at ASC, id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskEvent{}
	for rows.Next() {
		var event TaskEvent
		var data, created string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.AgentID, &event.EventType, &event.ThreadID, &event.FromState, &event.ToState, &data, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(data), &event.Data)
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, event)
	}
	return out, rows.Err()
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
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM tasks`+where+` GROUP BY state`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return out, err
		}
		switch state {
		case stateQueued:
			out.Queued = count
		case stateRunning:
			out.Running = count
		case stateWaiting:
			out.Waiting = count
		case stateBlocked:
			out.Blocked = count
		case stateCompleted:
			out.Completed = count
		case stateFailed:
			out.Failed = count
		case stateCancelled:
			out.Cancelled = count
		}
	}
	out.Active = out.Queued + out.Running + out.Waiting + out.Blocked
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tasks`+where+` AND schedule_kind<>'' AND schedule_enabled=1`, args...).Scan(&out.Scheduled)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tasks`+where+` AND schedule_kind<>'' AND schedule_enabled=0 AND state='waiting'`, args...).Scan(&out.Paused)
	return out, nil
}
