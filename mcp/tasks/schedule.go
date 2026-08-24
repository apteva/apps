package main

import (
	"errors"
	"fmt"
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
		return n, fmt.Errorf("invalid schedule timezone %q", n.Timezone)
	}
	if n.OverlapPolicy == "" {
		n.OverlapPolicy = "skip"
	}
	if n.CatchupPolicy == "" {
		n.CatchupPolicy = "skip"
	}
	if n.OverlapPolicy != "skip" || n.CatchupPolicy != "skip" {
		return n, errors.New("overlap_policy and catchup_policy currently support only skip")
	}
	switch n.Kind {
	case scheduleOnce:
		at, after := strings.TrimSpace(input.At), strings.TrimSpace(input.After)
		if (at == "") == (after == "") {
			return n, errors.New("once schedule requires exactly one of at or after")
		}
		var next time.Time
		if after != "" {
			d, parseErr := time.ParseDuration(after)
			if parseErr != nil || d < time.Minute {
				return n, errors.New("once after must be a duration of at least 1m")
			}
			next = now.Add(d)
		} else {
			next, err = time.Parse(time.RFC3339, at)
			if err != nil {
				return n, errors.New("once at must be RFC3339")
			}
		}
		next = next.UTC()
		if !next.After(now) {
			return n, errors.New("once schedule must be in the future")
		}
		n.Expression, n.NextRunAt = next.Format(time.RFC3339), next
	case scheduleInterval:
		d, parseErr := time.ParseDuration(strings.TrimSpace(input.Every))
		if parseErr != nil || d < time.Minute {
			return n, errors.New("interval every must be at least 1m")
		}
		n.Expression, n.NextRunAt = d.String(), now.Add(d)
	case scheduleCron:
		expr := strings.Join(strings.Fields(input.Cron), " ")
		parsed, parseErr := scheduleParser.Parse(expr)
		if parseErr != nil {
			return n, fmt.Errorf("invalid five-field cron: %w", parseErr)
		}
		next := parsed.Next(now.In(loc)).UTC()
		if next.IsZero() {
			return n, errors.New("cron has no future occurrence")
		}
		n.Expression, n.NextRunAt = expr, next
	default:
		return n, errors.New("schedule kind must be once, interval, or cron")
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
		return normalizedSchedule{}, false, errors.New("schedule kind is required when adding a schedule")
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
			next = now.Add(d)
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
		return nil, errors.New("task is not scheduled")
	}
}

func (s *taskStore) SetSchedule(id, actor string, input ScheduleInput) (*Task, error) {
	task, _, err := s.Update(id, actor, UpdateTaskInput{Schedule: &input})
	return task, err
}

func (s *taskStore) Pause(id, actor string) (*Task, error) {
	task, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if task.ScheduleKind == "" || terminalState(task.State) {
		return nil, errors.New("task is not an active schedule")
	}
	_, err = s.db.Exec(`UPDATE tasks SET schedule_enabled=0, updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, err
	}
	return s.recordSimpleEvent(id, actor, "schedule_paused", task.State, task.State, nil)
}

func (s *taskStore) Resume(id, actor string) (*Task, error) {
	task, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if task.ScheduleKind == "" || terminalState(task.State) {
		return nil, errors.New("task is not a resumable schedule")
	}
	input := ScheduleInput{Kind: task.ScheduleKind, Timezone: task.ScheduleTimezone, OverlapPolicy: task.ScheduleOverlapPolicy, CatchupPolicy: task.ScheduleCatchupPolicy}
	switch task.ScheduleKind {
	case scheduleOnce:
		input.At = task.ScheduleExpression
	case scheduleInterval:
		input.Every = task.ScheduleExpression
	case scheduleCron:
		input.Cron = task.ScheduleExpression
	}
	// A past one-time schedule is resumed as an immediate one-minute durable run.
	if task.ScheduleKind == scheduleOnce {
		input.At = ""
		input.After = "1m"
	}
	n, err := normalizeSchedule(input, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`UPDATE tasks SET schedule_enabled=1, next_run_at=?, updated_at=? WHERE id=?`, n.NextRunAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, err
	}
	return s.recordSimpleEvent(id, actor, "schedule_resumed", task.State, task.State, map[string]any{"next_run_at": n.NextRunAt})
}

func (s *taskStore) RunNow(id, actor string) (*Task, error) {
	task, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if task.ScheduleKind == "" || terminalState(task.State) {
		return nil, errors.New("task is not a runnable schedule")
	}
	now := time.Now().UTC()
	_, err = s.db.Exec(`UPDATE tasks SET schedule_enabled=1, next_run_at=?, updated_at=? WHERE id=?`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, err
	}
	return s.recordSimpleEvent(id, actor, "schedule_run_requested", task.State, task.State, nil)
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
	if current.ScheduleKind != scheduleOnce || !current.ScheduleEnabled || current.NextRunAt == nil || current.State != stateWaiting || !current.NextRunAt.Equal(scheduledFor) {
		return current, false, nil
	}

	now = now.UTC()
	scheduledFor = scheduledFor.UTC()
	step := "Scheduled work is ready"
	_, err = tx.Exec(`UPDATE tasks SET
		state=?, progress=NULL, current_step=?, schedule_enabled=0,
		scheduled_for=?, next_run_at=NULL, updated_at=?
		WHERE id=?`, stateQueued, step,
		scheduledFor.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, false, err
	}
	event, err := insertEvent(tx, TaskEvent{
		TaskID: id, AgentID: current.AgentID, EventType: "state_changed",
		ThreadID: actor, FromState: current.State, ToState: stateQueued,
		Data: map[string]any{
			"progress": nil, "current_step": step,
			"assigned_thread_id":  current.AssignedThreadID,
			"execution_thread_id": current.ExecutionThreadID,
			"schedule_enabled":    false, "next_run_at": nil,
			"scheduled_for": scheduledFor,
		},
	})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	updated, err := s.Get(id)
	if err == nil {
		s.emit(event)
	}
	return updated, true, err
}

type scheduler struct {
	store *taskStore
	app   *App
	mu    sync.Mutex
}

func (s *scheduler) Tick(now time.Time, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reconcileUnaccepted(now.UTC(), projectID); err != nil {
		return err
	}
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE schedule_enabled=1 AND next_run_at IS NOT NULL AND next_run_at<=?`
	args := []any{now.UTC().Format(time.RFC3339Nano)}
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

const dispatchAcceptanceTimeout = 10 * time.Minute

// reconcileUnaccepted closes the false-green gap where the scheduler wake was
// sent but no assigned thread ever loaded the authoritative occurrence. It is
// intentionally independent of the recurrence cadence and runs every tick.
func (s *scheduler) reconcileUnaccepted(now time.Time, projectID string) error {
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE state='queued' AND scheduled_for IS NOT NULL
		AND dispatched_at IS NOT NULL AND accepted_at IS NULL AND dispatched_at<=?`
	args := []any{now.Add(-dispatchAcceptanceTimeout).Format(time.RFC3339Nano)}
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
		failed := stateFailed
		msg := fmt.Sprintf("dispatched but not accepted by assigned thread within %s", dispatchAcceptanceTimeout)
		updated, _, updateErr := s.store.Update(run.ID, "tasks:scheduler", UpdateTaskInput{State: &failed, Error: &msg})
		if updateErr != nil {
			return updateErr
		}
		_ = s.app.notifyCreator(updated)
	}
	return nil
}

// An open run with no activity for staleRunMultiple cadences (clamped to
// [staleRunMinAge, staleRunMaxAge]) is treated as wedged and auto-failed, so a
// single stuck run cannot block every future occurrence of its schedule.
const (
	staleRunMultiple = 10
	staleRunMinAge   = time.Hour
	staleRunMaxAge   = 24 * time.Hour
)

func staleRunThreshold(cadence time.Duration) time.Duration {
	threshold := time.Duration(staleRunMultiple) * cadence
	if threshold < staleRunMinAge {
		return staleRunMinAge
	}
	if threshold > staleRunMaxAge {
		return staleRunMaxAge
	}
	return threshold
}

// scheduleCadence derives the cadence from the schedule expression itself, not
// from next_run_at bookkeeping — a blocked schedule's bookkeeping gaps grow with
// the blockage, which would inflate the stale threshold exactly when it matters.
func scheduleCadence(task *Task, now time.Time) time.Duration {
	switch task.ScheduleKind {
	case scheduleInterval:
		if d, err := time.ParseDuration(task.ScheduleExpression); err == nil {
			return d
		}
	case scheduleCron:
		if loc, err := time.LoadLocation(task.ScheduleTimezone); err == nil {
			if parsed, parseErr := scheduleParser.Parse(task.ScheduleExpression); parseErr == nil {
				first := parsed.Next(now.In(loc))
				return parsed.Next(first).Sub(first)
			}
		}
	}
	return 0
}

func (s *scheduler) openRuns(parentID string) ([]*Task, error) {
	rows, err := s.store.db.Query(`SELECT `+taskColumns+` FROM tasks WHERE parent_task_id=? AND state IN ('queued','running','waiting','blocked')`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []*Task{}
	for rows.Next() {
		run, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *scheduler) materialize(parent *Task, now time.Time) error {
	if parent.NextRunAt == nil {
		return nil
	}
	scheduledFor := parent.NextRunAt.UTC()
	if parent.ScheduleKind == scheduleOnce {
		run, activated, err := s.store.activateOneTime(parent.ID, "tasks:scheduler", scheduledFor, now)
		if err != nil {
			return err
		}
		if !activated {
			return nil
		}
		return s.dispatch(run, now, "task.ready")
	}
	next, err := nextOccurrence(parent, scheduledFor, now)
	if err != nil {
		return err
	}
	open, err := s.openRuns(parent.ID)
	if err != nil {
		return err
	}
	active := 0
	staleAfter := staleRunThreshold(scheduleCadence(parent, now))
	for _, run := range open {
		if idle := now.Sub(run.UpdatedAt); idle >= staleAfter {
			failed := stateFailed
			msg := fmt.Sprintf("run exceeded max age (no activity for %s, limit %s); auto-failed by scheduler", idle.Round(time.Second), staleAfter)
			updated, _, failErr := s.store.Update(run.ID, "tasks:scheduler", UpdateTaskInput{State: &failed, Error: &msg})
			if failErr != nil {
				s.app.logger().Warn("tasks scheduler stale run", "task_id", run.ID, "err", failErr.Error())
				active++
				continue
			}
			s.app.logger().Warn("tasks scheduler stale run", "task_id", run.ID, "idle", idle.String())
			_ = s.app.notifyCreator(updated)
			continue
		}
		active++
	}
	if active > 0 {
		// Advance only next_run_at: bumping last_run_at here would make a
		// blocked schedule look like it just ran.
		_, err = s.store.db.Exec(`UPDATE tasks SET next_run_at=?, updated_at=? WHERE id=?`, next.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), parent.ID)
		if err == nil {
			_, _ = s.store.recordSimpleEvent(parent.ID, "tasks:scheduler", "occurrence_skipped_overlap", parent.State, parent.State, map[string]any{"scheduled_for": scheduledFor})
		}
		return err
	}
	key := scheduledFor.Format(time.RFC3339Nano)
	run, _, err := s.store.Create(CreateTaskInput{AgentID: parent.AgentID, ProjectID: parent.ProjectID, Title: parent.Title, Description: parent.Description, State: stateQueued, CreatedByThreadID: parent.CreatedByThreadID, AssignedThreadID: parent.AssignedThreadID, ParentTaskID: parent.ID, ScheduledFor: &scheduledFor, OccurrenceKey: key, IdempotencyKey: parent.ID + ":" + key})
	if err != nil {
		return err
	}
	_, err = s.store.db.Exec(`UPDATE tasks SET next_run_at=?, updated_at=? WHERE id=?`, next.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), parent.ID)
	if err != nil {
		return err
	}
	return s.dispatch(run, now, "task.ready")
}

func (s *scheduler) dispatch(run *Task, now time.Time, eventType string) error {
	dispatched, _, err := s.store.MarkDispatched(run.ID, "tasks:scheduler", now)
	if err != nil {
		return err
	}
	if err := s.app.notifyAssigned(dispatched, dispatched.AssignedThreadID, eventType); err != nil {
		failed := stateFailed
		message := "scheduler dispatch failed: " + err.Error()
		updated, _, updateErr := s.store.Update(dispatched.ID, "tasks:scheduler", UpdateTaskInput{State: &failed, Error: &message})
		if updateErr == nil {
			_ = s.app.notifyCreator(updated)
		}
		if updateErr != nil {
			return fmt.Errorf("%s; recording failure: %w", message, updateErr)
		}
		return err
	}
	return nil
}
