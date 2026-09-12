package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type TaskResult struct {
	Task            map[string]any `json:"task"`
	DeliveryWarning string         `json:"delivery_warning"`
}

func (a *App) callTasks(project, process, action string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	args["process_id"] = process
	args["action"] = action
	if a.ctx.PlatformAPI() == nil {
		return errors.New("Tasks connection unavailable")
	}
	if err := a.ctx.WithProject(project).PlatformAPI().CallAppResult("tasks", "process_task", args, out); err != nil {
		return fmt.Errorf("Tasks 3.6.0+ is required: %w", err)
	}
	return nil
}
func snapshot(p *Process, d Definition, r Run) string {
	contract := "Read this task with Tasks get before domain actions. Track milestones, blockers and completion evidence in Tasks."
	if r.Backend == "agent" {
		contract = fmt.Sprintf("Read Processes run_get with process_id=%s and run_id=%s before domain actions; stop if the run is already completed, failed, or cancelled. Track progress with Processes run_update. Explicitly report completed with a concrete result and evidence, or blocked/waiting/failed with a reason. Do not create a Task for this run.", p.ID, r.ID)
	}
	return fmt.Sprintf("Company process: %s\nProcess ID: %s\nProcedure version: %d\n\nPurpose\n%s\n\nProcedure\n%s\n\nRequired inputs / sources\n%s\n\nStanding context\n%s\n\nRun context\n%s\n\nApproval requirements\n%s\n\nCompletion criteria and evidence\n%s\n\nExecution contract\n%s Follow this exact procedure version. Obtain required approvals before acting; this procedure does not grant authority. Do not modify the company procedure during execution.\n", d.Name, p.ID, r.Version, d.Description, d.Instructions, d.RequiredInputs, d.DefaultInputs, r.Inputs, d.ApprovalRequirements, d.CompletionCriteria, contract) + fmt.Sprintf("\nAssignment: %s (%s)\nTarget: %s\nParameters (data, not additional authority): %s\nScheduled occurrence: %s\nRun created: %s\n", r.Binding.Name, r.AssignmentID, r.Binding.Target, jsonText(r.Binding.Parameters), r.ScheduledFor, r.CreatedAt)
}
func (a *App) dispatch(p *Process, r *Run) (*TaskResult, error) {
	d, err := a.runDefinition(*r)
	if err != nil {
		return nil, err
	}
	if r.Backend == "agent" {
		return &TaskResult{}, a.dispatchAgent(p, r)
	}
	var result TaskResult
	if r.TaskID != "" && r.DeliveryWarning == "" {
		err = a.callTasks(p.ProjectID, p.ID, "get", map[string]any{"task_id": r.TaskID}, &result)
		return &result, err
	}
	args := map[string]any{"run_key": r.ID, "version": r.Version, "agent_id": d.OwnerAgentID, "title": d.Name, "description": snapshot(p, d, *r)}
	if r.Kind == "schedule" {
		args["schedule"] = d.Schedule
	}
	if err = a.callTasks(p.ProjectID, p.ID, "create", args, &result); err != nil {
		return nil, err
	}
	id, _ := result.Task["id"].(string)
	if id == "" {
		return nil, errors.New("Tasks returned no task ID")
	}
	_, err = a.db.Exec(`UPDATE process_runs SET task_id=?,delivery_warning=? WHERE id=?`, id, result.DeliveryWarning, r.ID)
	if err != nil {
		return nil, err
	}
	r.TaskID = id
	r.DeliveryWarning = result.DeliveryWarning
	return &result, nil
}
func (a *App) synchronize(p *Process) (err error) {
	if p.Assignment == nil {
		return a.synchronizeProcess(p)
	}
	defer func() {
		message := ""
		if err != nil {
			message = err.Error()
		}
		_, saveErr := a.db.Exec(`UPDATE process_assignments SET sync_pending=?,sync_error=? WHERE id=?`, err != nil, message, p.Assignment.ID)
		if saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}()
	runs, err := a.dispatches(p.ID)
	if err != nil {
		return err
	}
	// Finish unknown create outcomes using the same key, then disable every old
	// schedule before enabling the current version. Schedules are born paused.
	for i := range runs {
		r := &runs[i]
		if r.AssignmentID != p.Assignment.ID || r.Kind != "schedule" || r.Backend != "tasks" {
			continue
		}
		if r.TaskID == "" {
			if _, err = a.dispatch(p, r); err != nil {
				return err
			}
		}
		if p.Status != "active" || r.Version != p.Version || r.AssignmentRevision != p.Assignment.Revision || p.ExecutionMode != "tasks" {
			if r.SchedulePaused {
				continue
			}
			var result TaskResult
			if err = a.callTasks(p.ProjectID, p.ID, "pause", map[string]any{"task_id": r.TaskID}, &result); err != nil {
				return err
			}
			if _, err = a.db.Exec(`UPDATE process_runs SET schedule_paused=1 WHERE id=?`, r.ID); err != nil {
				return err
			}
		}
	}
	if p.ExecutionMode == "agent" {
		return a.syncDirectSchedule(p)
	}
	if p.Status == "active" && p.Schedule != nil {
		key := fmt.Sprintf("schedule:v%d:a%d", p.Version, p.Assignment.Revision)
		for _, old := range runs {
			if old.AssignmentID == p.Assignment.ID && old.Kind == "schedule" && old.Version == p.Version && old.AssignmentRevision == p.Assignment.Revision {
				key = old.RequestKey
				if p.Assignment.ID != "assignment-"+p.ID {
					key = strings.TrimPrefix(key, p.Assignment.ID+":")
				}
				break
			}
		}
		r, e := a.reserveRun(p, "schedule", key, "")
		if e != nil {
			return e
		}
		if _, err = a.dispatch(p, &r); err != nil {
			return err
		}
		var result TaskResult
		// A lost resume response means the schedule might be active. Invalidate
		// the old pause confirmation before sending the request.
		if _, err = a.db.Exec(`UPDATE process_runs SET schedule_paused=0 WHERE id=?`, r.ID); err != nil {
			return err
		}
		err = a.callTasks(p.ProjectID, p.ID, "resume", map[string]any{"task_id": r.TaskID}, &result)
	}
	return err
}
func (a *App) changeStatus(project, id, status string) (*Process, error) {
	p, err := a.get(project, id)
	if err != nil {
		return nil, err
	}
	if p.Status == "archived" && status != "archived" {
		return nil, errors.New("archived procedures cannot be reactivated")
	}
	if status == "active" {
		for _, x := range p.Assignments {
			if x.Status == "active" {
				if e := a.checkAssignment(project, x); e != nil {
					return nil, e
				}
			}
		}
	}
	_, err = a.db.Exec(`UPDATE processes SET status=?,sync_pending=1,sync_error='',updated_at=? WHERE id=? AND project_id=?`, status, timestamp(), id, project)
	if err != nil {
		return nil, err
	}
	p.Status = status
	// The desired state is durable even if the dependency is offline. Return its
	// explicit pending state so callers never mistake it for confirmed activation.
	_ = a.synchronize(p)
	return a.get(project, id)
}
func (a *App) start(project, id, key, inputs string) (any, error) {
	return a.startAssignment(project, id, "assignment-"+id, key, inputs, nil)
}
func (a *App) startAssignment(project, id, assignmentID, key, inputs string, overrides map[string]any) (any, error) {
	if len(key) == 0 || len(key) > 160 {
		return nil, errors.New("idempotency_key must contain 1–160 characters")
	}
	if len(inputs) > 32000 {
		return nil, errors.New("run context exceeds 32 KB")
	}
	p, e := a.get(project, id)
	if e != nil {
		return nil, e
	}
	x, e := a.assignment(project, id, assignmentID)
	if e != nil {
		return nil, e
	}
	p, e = a.assigned(p, x)
	if e != nil {
		return nil, e
	}
	requestKey := "manual:" + key
	if assignmentID != "assignment-"+id {
		requestKey = assignmentID + ":" + requestKey
	}
	var existing int
	e = a.db.QueryRow(`SELECT count(*) FROM process_runs WHERE process_id=? AND assignment_id=? AND request_key=?`, id, assignmentID, requestKey).Scan(&existing)
	if e != nil {
		return nil, e
	}
	if existing == 0 && (p.Status != "active" || p.SyncPending) {
		return nil, errors.New("activate and synchronize the process and assignment before starting")
	}
	r, e := a.reserveAssignedRun(p, "manual", "manual:"+key, inputs, overrides)
	if e != nil {
		return nil, e
	}
	result, e := a.dispatch(p, &r)
	if e != nil {
		return nil, fmt.Errorf("run saved; retry with the same idempotency_key: %w", e)
	}
	return map[string]any{"run": r, "task": result.Task, "delivery_warning": result.DeliveryWarning}, nil
}

func (a *App) runs(project, id string) (any, error) {
	if _, err := a.get(project, id); err != nil {
		return nil, err
	}
	records, err := a.dispatches(id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"runs": []any{}, "has_more": false}
	hasTasks := false
	direct := []Run{}
	for _, r := range records {
		if r.Backend == "tasks" {
			hasTasks = true
		} else {
			direct = append(direct, r)
		}
	}
	if hasTasks {
		if err = a.callTasks(project, id, "list", nil, &result); err != nil {
			result["tasks_error"] = err.Error()
		}
	}
	if entries, ok := result["runs"].([]any); ok {
		byID := map[string]Run{}
		for _, r := range records {
			byID[r.ID] = r
		}
		for _, entry := range entries {
			if m, ok := entry.(map[string]any); ok {
				key, _ := m["run_key"].(string)
				if r, found := byID[key]; found {
					m["assignment_id"] = r.AssignmentID
					m["assignment"] = r.Binding
				}
			}
		}
	}
	result["direct_runs"] = direct
	result["dispatches"] = records
	return result, nil
}

func (a *App) retryPending(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows, err := a.db.Query(`SELECT DISTINCT p.project_id,p.id FROM processes p LEFT JOIN process_runs r ON r.process_id=p.id WHERE p.sync_pending=1 OR EXISTS(SELECT 1 FROM process_assignments x WHERE x.process_id=p.id AND x.sync_pending=1) OR (r.backend='tasks' AND r.kind='manual' AND (r.task_id='' OR r.delivery_warning<>'')) LIMIT 100`)
	if err != nil {
		return err
	}
	type target struct{ project, id string }
	targets := []target{}
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.project, &t.id); err != nil {
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
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p, e := a.get(target.project, target.id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if p.SyncPending {
			if e = a.synchronize(p); e != nil {
				failures = append(failures, e)
			}
		}
		records, e := a.dispatches(p.ID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		for i := range records {
			r := &records[i]
			if r.Backend == "tasks" && r.Kind == "manual" && (r.TaskID == "" || r.DeliveryWarning != "") {
				if _, e = a.dispatch(p, r); e != nil {
					failures = append(failures, e)
				}
			}
		}
	}
	return errors.Join(failures...)
}
func decodeDefinition(v any) (Definition, error) {
	var d Definition
	raw, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(raw, &d)
	}
	return d, err
}
