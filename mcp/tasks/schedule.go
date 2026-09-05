package main

import (
	"database/sql"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	scheduleOnce     = "once"
	scheduleInterval = "interval"
	scheduleCron     = "cron"
)

type ScheduleInput struct {
	Kind          string `json:"kind"`
	At            string `json:"at,omitempty"`
	After         string `json:"after,omitempty"`
	Every         string `json:"every,omitempty"`
	Cron          string `json:"cron,omitempty"`
	Timezone      string `json:"timezone,omitempty"`
	OverlapPolicy string `json:"overlap_policy,omitempty"`
	CatchupPolicy string `json:"catchup_policy,omitempty"`
}

type normalizedSchedule struct {
	Kind, Expression, Timezone, OverlapPolicy, CatchupPolicy string
	NextRunAt                                                time.Time
}

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func normalizeSchedule(input ScheduleInput, now time.Time) (normalizedSchedule, error) {
	now = now.UTC()
	n := normalizedSchedule{Kind: strings.ToLower(strings.TrimSpace(input.Kind)), Timezone: strings.TrimSpace(input.Timezone), OverlapPolicy: strings.ToLower(strings.TrimSpace(input.OverlapPolicy)), CatchupPolicy: strings.ToLower(strings.TrimSpace(input.CatchupPolicy))}
	if n.Timezone == "" {
		n.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(n.Timezone)
	if err != nil {
		return n, validationError("invalid schedule timezone %q", n.Timezone)
	}
	if n.OverlapPolicy == "" {
		n.OverlapPolicy = "skip"
	}
	if n.CatchupPolicy == "" {
		n.CatchupPolicy = "skip"
	}
	if n.OverlapPolicy != "skip" || n.CatchupPolicy != "skip" {
		return n, validationError("overlap_policy and catchup_policy currently support only skip")
	}
	switch n.Kind {
	case scheduleOnce:
		at, after := strings.TrimSpace(input.At), strings.TrimSpace(input.After)
		if (at == "") == (after == "") {
			return n, validationError("once schedule requires exactly one of at or after")
		}
		var next time.Time
		if after != "" {
			d, parseErr := time.ParseDuration(after)
			if parseErr != nil || d < time.Minute {
				return n, validationError("once after must be a duration of at least 1m")
			}
			next = now.Add(d)
		} else {
			next, err = time.Parse(time.RFC3339, at)
			if err != nil {
				return n, validationError("once at must be RFC3339")
			}
		}
		next = next.UTC()
		if !next.After(now) {
			return n, validationError("once schedule must be in the future")
		}
		n.Expression, n.NextRunAt = next.Format(time.RFC3339), next
	case scheduleInterval:
		d, parseErr := time.ParseDuration(strings.TrimSpace(input.Every))
		if parseErr != nil || d < time.Minute {
			return n, validationError("interval every must be at least 1m")
		}
		n.Expression, n.NextRunAt = d.String(), now.Add(d)
	case scheduleCron:
		expr := strings.Join(strings.Fields(input.Cron), " ")
		parsed, parseErr := scheduleParser.Parse(expr)
		if parseErr != nil {
			return n, validationError("invalid five-field cron: %v", parseErr)
		}
		next := parsed.Next(now.In(loc)).UTC()
		if next.IsZero() {
			return n, validationError("cron has no future occurrence")
		}
		n.Expression, n.NextRunAt = expr, next
	default:
		return n, validationError("schedule kind must be once, interval, or cron")
	}
	return n, nil
}

// mergeSchedulePatch expands a partial definition edit against the persisted
// schedule. Empty fields preserve their current values. The scheduler's next
// timestamp only moves when cadence semantics change; metadata-only edits keep
// the existing timestamp.
func mergeSchedulePatch(task *Task, patch ScheduleInput, now time.Time) (normalizedSchedule, bool, error) {
	kind := strings.ToLower(strings.TrimSpace(patch.Kind))
	if kind == "" {
		kind = task.ScheduleKind
	}
	if kind == "" {
		return normalizedSchedule{}, false, validationError("schedule kind is required when adding a schedule")
	}
	timezone := strings.TrimSpace(patch.Timezone)
	if timezone == "" {
		timezone = task.ScheduleTimezone
	}
	overlap := strings.ToLower(strings.TrimSpace(patch.OverlapPolicy))
	if overlap == "" {
		overlap = task.ScheduleOverlapPolicy
	}
	catchup := strings.ToLower(strings.TrimSpace(patch.CatchupPolicy))
	if catchup == "" {
		catchup = task.ScheduleCatchupPolicy
	}

	full := ScheduleInput{Kind: kind, Timezone: timezone, OverlapPolicy: overlap, CatchupPolicy: catchup}
	sameKind := kind == task.ScheduleKind
	switch kind {
	case scheduleOnce:
		full.At, full.After = strings.TrimSpace(patch.At), strings.TrimSpace(patch.After)
		if full.At == "" && full.After == "" && sameKind {
			full.At = task.ScheduleExpression
		}
	case scheduleInterval:
		full.Every = strings.TrimSpace(patch.Every)
		if full.Every == "" && sameKind {
			full.Every = task.ScheduleExpression
		}
	case scheduleCron:
		full.Cron = strings.TrimSpace(patch.Cron)
		if full.Cron == "" && sameKind {
			full.Cron = task.ScheduleExpression
		}
	}

	n, err := normalizeSchedule(full, now)
	if err != nil {
		return n, false, err
	}
	changed := n.Kind != task.ScheduleKind || n.Expression != task.ScheduleExpression ||
		n.Timezone != task.ScheduleTimezone || n.OverlapPolicy != task.ScheduleOverlapPolicy ||
		n.CatchupPolicy != task.ScheduleCatchupPolicy
	if !changed {
		if task.NextRunAt != nil {
			n.NextRunAt = *task.NextRunAt
		}
		return n, false, nil
	}

	// Timezone and policy metadata do not shift interval or absolute one-time
	// cadence. Cron timezone changes do because they alter the next wall-clock run.
	cadenceChanged := n.Kind != task.ScheduleKind || n.Expression != task.ScheduleExpression ||
		(n.Kind == scheduleCron && n.Timezone != task.ScheduleTimezone)
	if !cadenceChanged && task.NextRunAt != nil {
		n.NextRunAt = *task.NextRunAt
	}
	return n, true, nil
}

func nextOccurrence(task *Task, scheduledFor, now time.Time) (*time.Time, error) {
	switch task.ScheduleKind {
	case scheduleOnce:
		return nil, nil
	case scheduleInterval:
		d, err := time.ParseDuration(task.ScheduleExpression)
		if err != nil {
			return nil, err
		}
		next := scheduledFor.Add(d)
		if !next.After(now) {
			// Skip missed runs while preserving the original interval phase.
			next = now.Add(d - now.Sub(scheduledFor)%d)
		}
		return &next, nil
	case scheduleCron:
		loc, err := time.LoadLocation(task.ScheduleTimezone)
		if err != nil {
			return nil, err
		}
		parsed, err := scheduleParser.Parse(task.ScheduleExpression)
		if err != nil {
			return nil, err
		}
		next := parsed.Next(now.In(loc)).UTC()
		return &next, nil
	default:
		return nil, validationError("task is not scheduled")
	}
}

func (s *taskStore) SetSchedule(id, actor string, input ScheduleInput) (*Task, error) {
	task, _, err := s.Update(id, actor, UpdateTaskInput{Schedule: &input})
	return task, err
}

func (s *taskStore) Pause(id, actor string) (*Task, error) {
	return s.changeScheduleEnabled(id, actor, false)
}
func (s *taskStore) Resume(id, actor string) (*Task, error) {
	return s.changeScheduleEnabled(id, actor, true)
}
func (s *taskStore) changeScheduleEnabled(id, actor string, enabled bool) (*Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if !isScheduleDefinitionTask(task) {
		return nil, validationError("task is not an active schedule definition")
	}
	if task.ScheduleEnabled == enabled {
		return task, nil
	}
	now := s.now()
	next := task.NextRunAt
	if enabled && (next == nil || !next.After(now)) {
		if task.ScheduleKind == scheduleOnce {
			value := now.Add(time.Minute)
			next = &value
		} else {
			next, err = nextOccurrence(task, now, now)
			if err != nil {
				return nil, err
			}
		}
	}
	var nextValue any
	if next != nil {
		nextValue = next.Format(timeFormat)
	}
	if _, err = tx.Exec(`UPDATE tasks SET schedule_enabled=?, next_run_at=?,updated_at=? WHERE id=?`, enabled, nextValue, now.Format(timeFormat), id); err != nil {
		return nil, err
	}
	kind := "schedule_paused"
	if enabled {
		kind = "schedule_resumed"
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: task.AgentID, EventType: kind, ThreadID: actor, FromState: task.State, ToState: task.State, Data: map[string]any{"next_run_at": nextValue}})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.emit(event)
	return s.Get(id)
}

// Manual occurrences consume no recurring cadence and do not resume a paused series.
func (s *taskStore) RunNow(id, actor string) (*Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	parent, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if !isScheduleDefinitionTask(parent) {
		return nil, validationError("task is not a runnable schedule definition")
	}
	now := s.now()
	var events []TaskEvent
	var run *Task
	if parent.ScheduleKind == scheduleOnce {
		run, err = s.activateOneTimeTx(tx, parent, actor, now, now, &events)
	} else {
		var active int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE parent_task_id=? AND state IN ('queued','running','waiting','blocked')`, id).Scan(&active); err != nil {
			return nil, err
		}
		if active > 0 {
			return nil, validationError("schedule already has an open occurrence")
		}
		run, _, err = s.createTx(tx, CreateTaskInput{AgentID: parent.AgentID, ProjectID: parent.ProjectID, Title: parent.Title, Description: parent.Description, CreatedByThreadID: parent.CreatedByThreadID, AssignedThreadID: parent.AssignedThreadID, ParentTaskID: parent.ID, ScheduledFor: &now, OccurrenceKey: "manual:" + newID(""), OperationKey: parent.OperationKey}, now, &events)
	}
	if err != nil {
		return nil, err
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: parent.AgentID, EventType: "schedule_run_requested", ThreadID: actor, FromState: parent.State, ToState: parent.State, Data: map[string]any{"occurrence_id": run.ID}})
	if err != nil {
		return nil, err
	}
	events = append(events, event)
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for _, event := range events {
		s.emit(event)
	}
	return run, nil
}

func (s *taskStore) recordSimpleEvent(id, actor, eventType, from, to string, data map[string]any) (*Task, error) {
	task, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	event, err := insertEvent(tx, TaskEvent{TaskID: id, AgentID: task.AgentID, EventType: eventType, ThreadID: actor, FromState: from, ToState: to, Data: data})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.emit(event)
	return s.Get(id)
}

// activateOneTime turns a due one-time schedule into its runnable task in a
// single transaction. The event is emitted only after every scheduling field
// is committed, so an event-driven UI reload cannot observe a queued task that
// still appears to have an active schedule.
func (s *taskStore) activateOneTime(id, actor string, scheduledFor, now time.Time) (*Task, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	current, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if !isScheduleDefinitionTask(current) || current.ScheduleKind != scheduleOnce || !current.ScheduleEnabled || current.NextRunAt == nil || !current.NextRunAt.Equal(scheduledFor) {
		return current, false, nil
	}
	var events []TaskEvent
	task, err := s.activateOneTimeTx(tx, current, actor, scheduledFor, now, &events)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	for _, event := range events {
		s.emit(event)
	}
	return task, true, nil
}

func (s *taskStore) activateOneTimeTx(tx *sql.Tx, current *Task, actor string, scheduledFor, now time.Time, events *[]TaskEvent) (*Task, error) {
	_, err := tx.Exec(`UPDATE tasks SET state='queued',progress=NULL,current_step='Scheduled work is ready',schedule_enabled=0,scheduled_for=?,next_run_at=NULL,updated_at=? WHERE id=?`, scheduledFor.UTC().Format(timeFormat), now.UTC().Format(timeFormat), current.ID)
	if err != nil {
		return nil, err
	}
	task, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, current.ID))
	if err != nil {
		return nil, err
	}
	event, err := insertEvent(tx, TaskEvent{TaskID: current.ID, AgentID: current.AgentID, EventType: "state_changed", ThreadID: actor, FromState: current.State, ToState: stateQueued, Data: map[string]any{"current_step": task.CurrentStep, "schedule_enabled": false, "next_run_at": nil, "scheduled_for": scheduledFor}})
	if err != nil {
		return nil, err
	}
	if err = enqueueCreatedTaskTx(tx, task, now); err != nil {
		return nil, err
	}
	*events = append(*events, event)
	return task, nil
}

type scheduler struct {
	store *taskStore
	app   *App
	mu    sync.Mutex
}

func (s *scheduler) Tick(now time.Time, projectID string) error {
	err := s.tickState(now, projectID)
	deliveryErr := s.app.drainDeliveries("", projectID, now)
	if deliveryErr != nil {
		s.app.logger().Warn("tasks pending deliveries", "err", deliveryErr)
	}
	return err
}
func (s *scheduler) tickState(now time.Time, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.store.FailExpiredTerminalizations(now.UTC(), projectID); err != nil {
		return err
	}
	if _, err := s.store.ClaimSettledTerminalizations(now.UTC(), projectID); err != nil {
		return err
	}
	if err := s.reconcileUnaccepted(now.UTC(), projectID); err != nil {
		return err
	}
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE state='waiting' AND scheduled_for IS NULL AND schedule_enabled=1 AND next_run_at IS NOT NULL AND next_run_at<=?`
	args := []any{now.UTC().Format(timeFormat)}
	if projectID != "" {
		query += ` AND project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY next_run_at ASC LIMIT 100`
	rows, err := s.store.db.Query(query, args...)
	if err != nil {
		return err
	}
	due := []*Task{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			rows.Close()
			return e
		}
		due = append(due, t)
	}
	rows.Close()
	for _, task := range due {
		if err := s.materialize(task, now.UTC()); err != nil {
			s.app.logger().Warn("tasks scheduler", "task_id", task.ID, "err", err.Error())
		}
	}
	return nil
}

const (
	dispatchRetryInterval = 10 * time.Minute
	dispatchMaxAttempts   = 3 // Initial task.ready plus two durable retries.
)

// reconcileUnaccepted closes the false-green gap where the scheduler wake was
// sent but no assigned thread ever loaded the authoritative occurrence. It
// retries the same durable task.ready event twice before failing the occurrence.
// The policy is independent of recurrence cadence and runs every tick.
func (s *scheduler) reconcileUnaccepted(now time.Time, projectID string) error {
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE state='queued' AND scheduled_for IS NOT NULL
		AND dispatched_at IS NOT NULL AND accepted_at IS NULL
		AND COALESCE(last_dispatch_attempt_at, dispatched_at)<=?`
	eligibleBefore := now.Add(-dispatchRetryInterval)
	args := []any{eligibleBefore.Format(timeFormat)}
	if projectID != "" {
		query += ` AND project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.store.db.Query(query, args...)
	if err != nil {
		return err
	}
	var overdue []*Task
	for rows.Next() {
		run, scanErr := scanTask(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		overdue = append(overdue, run)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, run := range overdue {
		updated, action, reconcileErr := s.store.ReconcileUnacceptedDispatch(run.ID, "tasks:scheduler", now,
			eligibleBefore, dispatchMaxAttempts)
		if reconcileErr != nil {
			return reconcileErr
		}
		switch action {
		case dispatchReconcileFail:
			s.app.logger().Warn("tasks scheduler unaccepted dispatch exhausted", "task_id", updated.ID,
				"attempts", updated.DispatchAttempts)
			// The terminal receipt is in the same transaction as failure.
		}
	}
	return nil
}

func (s *scheduler) materialize(snapshot *Task, now time.Time) error {
	if snapshot.NextRunAt == nil {
		return nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	parent, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, snapshot.ID))
	if err != nil {
		return err
	}
	if !isScheduleDefinitionTask(parent) || !parent.ScheduleEnabled || parent.NextRunAt == nil || !parent.NextRunAt.Equal(*snapshot.NextRunAt) || !parent.UpdatedAt.Equal(snapshot.UpdatedAt) {
		return nil
	}
	scheduledFor := parent.NextRunAt.UTC()
	var events []TaskEvent
	if parent.ScheduleKind == scheduleOnce {
		if _, err = s.store.activateOneTimeTx(tx, parent, "tasks:scheduler", scheduledFor, now, &events); err != nil {
			return err
		}
	} else {
		next, err := nextOccurrence(parent, scheduledFor, now)
		if err != nil {
			return err
		}
		var active int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE parent_task_id=? AND state IN ('queued','running','waiting','blocked')`, parent.ID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			// A business-progress timestamp is not a lease. Only explicit terminal
			// outcomes or tracked execution reconciliation can release the overlap guard.
			event, err := insertEvent(tx, TaskEvent{TaskID: parent.ID, AgentID: parent.AgentID, EventType: "occurrence_skipped_overlap", ThreadID: "tasks:scheduler", FromState: parent.State, ToState: parent.State, Data: map[string]any{"scheduled_for": scheduledFor}})
			if err != nil {
				return err
			}
			events = append(events, event)
		} else {
			// Occurrence identity retains the canonical pre-upgrade representation.
			key := scheduledFor.Format(time.RFC3339Nano)
			if _, _, err = s.store.createTx(tx, CreateTaskInput{AgentID: parent.AgentID, ProjectID: parent.ProjectID, Title: parent.Title, Description: parent.Description, CreatedByThreadID: parent.CreatedByThreadID, AssignedThreadID: parent.AssignedThreadID, ParentTaskID: parent.ID, ScheduledFor: &scheduledFor, OccurrenceKey: key, IdempotencyKey: parent.ID + ":" + key, OperationKey: parent.OperationKey}, now, &events); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`UPDATE tasks SET next_run_at=?,updated_at=? WHERE id=?`, next.Format(timeFormat), now.Format(timeFormat), parent.ID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, event := range events {
		s.store.emit(event)
	}
	return nil
}
