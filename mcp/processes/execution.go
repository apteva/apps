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
	return fmt.Sprintf("Company process: %s\nProcess ID: %s\nProcedure version: %d\n\nPurpose\n%s\n\nProcedure\n%s\n\nRequired inputs / sources\n%s\n\nStanding context\n%s\n\nRun context\n%s\n\nApproval requirements\n%s\n\nCompletion criteria and evidence\n%s\n\nExecution contract\nRead this task with Tasks get before domain actions. Follow this exact procedure version. Track milestones, blockers, delegation, and completion evidence in Tasks. Do not change the company procedure during execution. Obtain required approvals before the corresponding action; these instructions do not grant authority. If inputs or approval are missing, record the blocker and request them. Do not create another task for this same run.\n", d.Name, p.ID, r.Version, d.Description, d.Instructions, d.RequiredInputs, d.DefaultInputs, r.Inputs, d.ApprovalRequirements, d.CompletionCriteria)
}
func (a *App) dispatch(p *Process, r *Run) (*TaskResult, error) {
	d, err := a.definition(p.ID, r.Version)
	if err != nil {
		return nil, err
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
	defer func() {
		message := ""
		if err != nil {
			message = err.Error()
		}
		_, saveErr := a.db.Exec(`UPDATE processes SET sync_pending=?,sync_error=? WHERE id=?`, err != nil, message, p.ID)
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
		if r.Kind != "schedule" {
			continue
		}
		if r.TaskID == "" {
			if _, err = a.dispatch(p, r); err != nil {
				return err
			}
		}
		if p.Status != "active" || r.Version != p.Version {
			var result TaskResult
			if err = a.callTasks(p.ProjectID, p.ID, "pause", map[string]any{"task_id": r.TaskID}, &result); err != nil {
				return err
			}
		}
	}
	if p.Status == "active" && p.Schedule != nil {
		r, e := a.reserveRun(p, "schedule", fmt.Sprintf("schedule:v%d", p.Version), "")
		if e != nil {
			return e
		}
		if _, err = a.dispatch(p, &r); err != nil {
			return err
		}
		var result TaskResult
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
		agent, e := a.ctx.GetAgent(p.OwnerAgentID)
		if e != nil {
			return nil, e
		}
		if agent.ProjectID != project || strings.TrimSpace(agent.DefaultThreadID) == "" {
			return nil, errors.New("owner needs a default thread in this project")
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
	if len(key) == 0 || len(key) > 160 {
		return nil, errors.New("idempotency_key must contain 1–160 characters")
	}
	if len(inputs) > 32000 {
		return nil, errors.New("run context exceeds 32 KB")
	}
	p, err := a.get(project, id)
	if err != nil {
		return nil, err
	}
	existing, err := a.dispatches(id)
	if err != nil {
		return nil, err
	}
	retry := false
	for _, r := range existing {
		if r.RequestKey == "manual:"+key {
			retry = true
		}
	}
	if !retry && (p.Status != "active" || p.SyncPending) {
		return nil, errors.New("activate and synchronize the procedure before starting")
	}
	r, err := a.reserveRun(p, "manual", "manual:"+key, inputs)
	if err != nil {
		return nil, err
	}
	result, err := a.dispatch(p, &r)
	if err != nil {
		return nil, fmt.Errorf("run saved; retry with the same idempotency_key: %w", err)
	}
	return map[string]any{"run": r, "task": result.Task, "delivery_warning": result.DeliveryWarning}, nil
}
func (a *App) runs(project, id string) (any, error) {
	if _, err := a.get(project, id); err != nil {
		return nil, err
	}
	// Task status and results are read live, never maintained in a second ledger.
	var result map[string]any
	err := a.callTasks(project, id, "list", nil, &result)
	if err != nil {
		return nil, err
	}
	records, err := a.dispatches(id)
	if err != nil {
		return nil, err
	}
	result["dispatches"] = records
	return result, nil
}
func (a *App) retryPending(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows, err := a.db.Query(`SELECT DISTINCT p.project_id,p.id FROM processes p LEFT JOIN process_runs r ON r.process_id=p.id WHERE p.sync_pending=1 OR (r.kind='manual' AND (r.task_id='' OR r.delivery_warning<>'')) LIMIT 100`)
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
			if r.Kind == "manual" && (r.TaskID == "" || r.DeliveryWarning != "") {
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
