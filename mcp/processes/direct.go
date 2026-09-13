package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/robfig/cron/v3"
	"strings"
	"time"
)

func terminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}
func nextDirect(s *Schedule, now time.Time) (string, error) {
	if s == nil {
		return "", nil
	}
	var next time.Time
	if s.Kind == "interval" {
		d, e := time.ParseDuration(s.Every)
		if e != nil {
			return "", e
		}
		next = now.Add(d)
	} else {
		loc, e := time.LoadLocation(s.Timezone)
		if e != nil {
			return "", e
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, e := parser.Parse(s.Cron)
		if e != nil {
			return "", e
		}
		next = schedule.Next(now.In(loc))
	}
	if next.IsZero() {
		return "", errors.New("schedule has no next occurrence")
	}
	return next.UTC().Format(time.RFC3339Nano), nil
}
func (a *App) syncDirectSchedule(p *Process) error {
	next := ""
	var err error
	if p.Status == "active" && p.Schedule != nil {
		next = p.NextRunAt
		if next == "" || p.ScheduledVersion != p.Version {
			next, err = nextDirect(p.Schedule, time.Now().UTC())
			if err != nil {
				return err
			}
		}
	}
	_, err = a.db.Exec(`UPDATE process_assignments SET next_run_at=?,scheduled_version=? WHERE id=?`, next, p.Version, p.Assignment.ID)
	return err
}
func (a *App) dispatchAgent(p *Process, r *Run) (err error) {
	if r.DeliveredAt != "" || terminal(r.State) {
		return nil
	}
	if r.NextAttemptAt != "" {
		next, e := time.Parse(time.RFC3339Nano, r.NextAttemptAt)
		if e == nil && time.Now().Before(next) {
			return errors.New("delivery retry pending; reuse the same key")
		}
	}
	d, err := a.runDefinition(*r)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			delay := 30 * time.Second
			for i := 0; i < r.DeliveryAttempts && delay < 15*time.Minute; i++ {
				delay *= 2
			}
			if delay > 15*time.Minute {
				delay = 15 * time.Minute
			}
			_, saveErr := a.db.Exec(`UPDATE process_runs SET delivery_warning=?,delivery_attempts=delivery_attempts+1,next_attempt_at=? WHERE id=?`, err.Error(), time.Now().Add(delay).UTC().Format(time.RFC3339Nano), r.ID)
			err = errors.Join(err, saveErr)
		}
	}()
	if r.TargetThreadID == "" {
		agent, e := a.ctx.GetAgent(d.OwnerAgentID)
		if e != nil {
			return e
		}
		if agent.ProjectID != p.ProjectID || agent.DefaultThreadID == "" {
			return errors.New("owner needs a default thread in this project")
		}
		r.TargetThreadID = agent.DefaultThreadID
		if _, err = a.db.Exec(`UPDATE process_runs SET target_thread_id=? WHERE id=?`, r.TargetThreadID, r.ID); err != nil {
			return err
		}
	}
	events := a.ctx.WithProject(p.ProjectID).AgentEventsAPI()
	if events == nil {
		return errors.New("tracked agent delivery unavailable")
	}
	receipt, err := events.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: d.OwnerAgentID, ThreadID: r.TargetThreadID, SourceEventID: "processes:" + r.ID, Message: snapshot(p, d, *r)})
	if err != nil {
		return err
	}
	if !receipt.Accepted && !receipt.Duplicate {
		return errors.New("agent did not accept the run")
	}
	r.DeliveredAt = timestamp()
	r.ExecutionID = receipt.ExecutionID
	r.DeliveryWarning = ""
	_, err = a.db.Exec(`UPDATE process_runs SET delivered_at=?,execution_id=?,delivery_warning='',next_attempt_at='',delivery_attempts=delivery_attempts+1 WHERE id=?`, r.DeliveredAt, r.ExecutionID, r.ID)
	return err
}
func (a *App) directRun(project, actor, process, id, action string, args map[string]any) (any, error) {
	r, err := a.getRun(project, process, id)
	if err != nil {
		return nil, err
	}
	if r.Backend != "agent" && !r.Workflow {
		return nil, errors.New("use Tasks to track this run")
	}
	d, err := a.runDefinition(r)
	if err != nil {
		return nil, err
	}
	if r.Workflow {
		if action == "run_update" {
			return nil, errors.New("structured run outcomes are derived from its steps; use step_update")
		}
		steps, e := a.steps(r.ID)
		if e != nil {
			return nil, e
		}
		return map[string]any{"run": r, "definition": d, "steps": steps}, nil
	}
	if action == "run_update" {
		if actor != "operator" && !strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", d.OwnerAgentID)) {
			return nil, errors.New("only the run owner can update it")
		}
		state := str(args, "state")
		switch state {
		case "running", "waiting", "blocked", "completed", "failed", "cancelled":
		default:
			return nil, errors.New("invalid run state")
		}
		progress := r.Progress
		if v, ok := args["progress"]; ok {
			n, valid := v.(float64)
			if !valid || n < 0 || n > 100 || n != float64(int(n)) {
				return nil, errors.New("progress must be an integer from 0 to 100")
			}
			progress = int(n)
		}
		step, result, reason := r.CurrentStep, r.Result, r.Error
		if _, ok := args["current_step"]; ok {
			step = str(args, "current_step")
		}
		if _, ok := args["result"]; ok {
			result = str(args, "result")
		}
		if _, ok := args["error"]; ok {
			reason = str(args, "error")
		}
		if len(step)+len(result)+len(reason) > 64000 {
			return nil, errors.New("run update exceeds 64 KB")
		}
		if state == "completed" {
			if e := a.requiredTasksComplete(r.ID); e != nil {
				return nil, e
			}
			if result == "" {
				return nil, errors.New("completion requires a result with evidence")
			}
			progress = 100
		}
		if (state == "failed" || state == "cancelled" || state == "blocked" || state == "waiting") && reason == "" && step == "" {
			return nil, errors.New("record the reason or blocker")
		}
		if terminal(r.State) {
			if state != r.State || progress != r.Progress || step != r.CurrentStep || result != r.Result || reason != r.Error {
				return nil, errors.New("terminal run outcome is immutable")
			}
		} else {
			_, err = a.db.Exec(`UPDATE process_runs SET state=?,progress=?,current_step=?,result=?,error=? WHERE id=?`, state, progress, step, result, reason, id)
			if err != nil {
				return nil, err
			}
		}
		r, err = a.getRun(project, process, id)
		if err != nil {
			return nil, err
		}
	}
	p, err := a.get(project, process)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": r, "definition": d, "snapshot": snapshot(p, d, r)}, nil
}

// One transaction records each due occurrence and advances its deadline. Missed
// intervals are skipped and an outstanding scheduled run prevents overlap.
func (a *App) dueDirect(p *Process, now time.Time) error {
	if p.Assignment == nil {
		x, e := a.assignment(p.ProjectID, p.ID, "assignment-"+p.ID)
		if e != nil {
			return e
		}
		p, e = a.assigned(p, x)
		if e != nil {
			return e
		}
	}
	if p.Status != "active" || p.SyncPending || (p.ExecutionMode != "agent" && len(p.Steps) == 0) || p.Schedule == nil || p.NextRunAt == "" {
		return nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, p.NextRunAt)
	if err != nil {
		return err
	}
	if deadline.After(now) {
		return nil
	}
	next, err := nextDirect(p.Schedule, now)
	if err != nil {
		return err
	}
	binding := p.Assignment.AssignmentConfig
	if e := a.validateRoles(p.ProjectID, p.Definition, binding); e != nil {
		return e
	}
	binding.Roles = resolvedRoles(p.Definition, binding)
	binding.Parameters, err = validateParameters(p.Parameters, binding.Parameters, true)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var outstanding int
	if err = tx.QueryRow(`SELECT count(*) FROM process_runs WHERE assignment_id=? AND (backend='agent' OR workflow=1) AND scheduled_for<>'' AND state NOT IN ('completed','failed','cancelled')`, p.Assignment.ID).Scan(&outstanding); err != nil {
		return err
	}
	note := ""
	if outstanding == 0 {
		_, err = tx.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,inputs,created_at,backend,scheduled_for,assignment_id,assignment_revision,assignment_json,workflow) VALUES(?,?,?,'manual',?,'',?,?,?,?,?,?,?)`, newID("run-"), p.ID, p.Version, fmt.Sprintf("%s:scheduled:v%d:%s", p.Assignment.ID, p.Version, p.NextRunAt), timestamp(), p.ExecutionMode, p.NextRunAt, p.Assignment.ID, p.Assignment.Revision, jsonText(binding), len(p.Steps) > 0)
		if err != nil {
			return err
		}
	} else {
		note = "Skipped an occurrence because a previous scheduled run is still open."
	}
	_, err = tx.Exec(`UPDATE process_assignments SET next_run_at=?,last_schedule_note=? WHERE id=?`, next, note, p.Assignment.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) tickDirect(ctx context.Context, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows, err := a.db.Query(`SELECT id,project_id FROM processes`)
	if err != nil {
		return err
	}
	type target struct{ id, project string }
	targets := []target{}
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.id, &t.project); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p, e := a.get(t.project, t.id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		for _, x := range p.Assignments {
			v, e := a.assigned(p, x)
			if e == nil {
				e = a.dueDirect(v, now)
			}
			if e != nil {
				failures = append(failures, e)
			}
		}
		runs, e := a.dispatches(p.ID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		for i := range runs {
			r := &runs[i]
			if r.Workflow {
				if !terminal(r.State) {
					if e = a.reconcileWorkflow(p, r); e != nil {
						failures = append(failures, e)
					}
				}
				continue
			}
			if r.Backend != "agent" || r.DeliveredAt != "" || terminal(r.State) {
				continue
			}
			if next, e := time.Parse(time.RFC3339Nano, r.NextAttemptAt); e == nil && next.After(now) {
				continue
			}
			if e = a.dispatchAgent(p, r); e != nil {
				failures = append(failures, e)
			}
		}
	}
	failures = append(failures, a.tickNativeTasksLocked(ctx))
	return errors.Join(failures...)
}

func (a *App) EventHandlers() []sdk.EventHandler {
	return append(a.lifecycleHandlers(), sdk.EventHandler{Event: sdk.AppBusDeliveryEvent, Handler: a.receiveTriggerEvent})
}
func (a *App) lifecycleHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{{Event: sdk.AgentEventLifecycleEvent, Handler: func(ctx *sdk.AppCtx, event sdk.Event) error {
		lifecycle, err := sdk.DecodeAgentEventLifecycle(event)
		if err != nil {
			return err
		}
		if strings.HasPrefix(lifecycle.SourceEventID, "process-step:") {
			return a.stepLifecycle(event, lifecycle)
		}
		if !strings.HasPrefix(lifecycle.SourceEventID, "processes:") {
			return nil
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		id := strings.TrimPrefix(lifecycle.SourceEventID, "processes:")
		var process string
		if err = a.db.QueryRow(`SELECT process_id FROM process_runs WHERE id=? AND backend='agent'`, id).Scan(&process); err != nil {
			return nil
		}
		r, err := a.getRun(event.ProjectID, process, id)
		if err != nil {
			return err
		}
		d, err := a.runDefinition(r)
		if err != nil {
			return err
		}
		if event.SourceApp != "apteva-server" || event.InstanceID != d.OwnerAgentID {
			return errors.New("run lifecycle source mismatch")
		}
		if int64(lifecycle.Sequence) <= r.LifecycleSequence {
			return nil
		}
		// Transport settlement does not prove the business outcome. Only run_update
		// completes a process; lifecycle is diagnostic execution information.
		_, err = a.db.Exec(`UPDATE process_runs SET execution_state=?,lifecycle_sequence=?,execution_id=?,delivered_at=CASE WHEN delivered_at='' THEN ? ELSE delivered_at END,delivery_warning='',next_attempt_at='' WHERE id=?`, lifecycle.Type, lifecycle.Sequence, lifecycle.ExecutionID, timestamp(), id)
		return err
	}}}
}
