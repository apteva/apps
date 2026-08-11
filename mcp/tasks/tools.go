package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": false}
}

func scheduleSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"kind"}, "properties": map[string]any{
		"kind":     map[string]any{"type": "string", "enum": []string{"once", "interval", "cron"}},
		"at":       map[string]any{"type": "string", "description": "RFC3339 timestamp for a one-time schedule."},
		"after":    map[string]any{"type": "string", "description": "Relative duration such as 10m; server time is authoritative."},
		"every":    map[string]any{"type": "string", "description": "Interval such as 1h or 24h."},
		"cron":     map[string]any{"type": "string", "description": "Five-field cron expression."},
		"timezone": map[string]any{"type": "string", "description": "IANA timezone, default UTC."},
	}}
}

func (a *App) tools() []sdk.Tool {
	wakeAlways := map[string]any{"io.apteva/wakeOnResult": "always"}
	return []sdk.Tool{
		{Name: "create", Description: "Create exactly one durable task before substantive work begins when an outcome is multi-step, combines multiple sources or independent checks, is scheduled, delegated, or must survive the current exchange. A multi-area review or synthesis is task work even when calls run in parallel or finish in one turn. A quick lookup means one bounded read or action with no multi-source synthesis. Creating a task does not imply delegation: the current opaque thread may create, execute, update, and complete its own task. The current thread becomes creator; immediate work defaults to that creator, while scheduled work defaults to the agent's configured default thread. assigned_thread_id overrides either default.", InputSchema: objectSchema([]string{"title"}, map[string]any{
			"title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "assigned_thread_id": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}, "schedule": scheduleSchema(),
		}), Meta: wakeAlways, HandlerCtx: a.toolCreate},
		{Name: "list", Description: "List this agent's durable task inventory across all of its threads. Tasks are independent of conversations; creator, assignee, and executor thread IDs are provenance only and never limit visibility. Use this directly for task and schedule questions instead of asking another thread. The result is authoritative; do not repeat the same list call.", InputSchema: objectSchema(nil, map[string]any{
			"states": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "include_runs": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"},
		}), HandlerCtx: a.toolList},
		{Name: "get", Description: "Get a task and its chronological event history. Read this before resuming assigned work.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), HandlerCtx: a.toolGet},
		{Name: "update", Description: "Record task-level state and progress at meaningful phase boundaries. Keep the task running while any executor is actively working, including a delegated worker. Use waiting only when no executor can progress until an external event; when work resumes, return to running with a concrete current_step. Multi-stage or delegated work should usually have at least two or three intermediate milestones, while short work may move directly from its first update to completion. Do not update after every tool call or mirror task progress into global status.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "state": map[string]any{"type": "string", "enum": []string{"queued", "running", "waiting", "blocked", "failed"}}, "progress": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "current_step": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}, "schedule": scheduleSchema(),
		}), Meta: wakeAlways, HandlerCtx: a.toolUpdate},
		{Name: "assign", Description: "Assign a task to an existing opaque thread id belonging to the same agent. This records ownership and sends the assignment event; it never creates a thread. Main must use the platform spawn tool first when a new worker is needed.", InputSchema: objectSchema([]string{"task_id", "thread_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "thread_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolAssign},
		{Name: "complete", Description: "Complete an assigned task once with its concrete result. Completion sets progress to 100 and current_step to Completed. Tasks sends a structured terminal receipt to its creator thread when different; do not duplicate that delivery.", InputSchema: objectSchema([]string{"task_id", "result"}, map[string]any{"task_id": map[string]any{"type": "string"}, "result": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolComplete},
		{Name: "cancel", Description: "Cancel an active task or scheduled series.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolCancel},
		{Name: "pause", Description: "Pause a scheduled task without deleting it.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolPause},
		{Name: "resume", Description: "Resume a paused scheduled task and calculate its next occurrence from server time.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolResume},
		{Name: "run_now", Description: "Make a scheduled task due now without changing its recurrence.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolRunNow},
	}
}

func callIdentity(ctx context.Context, app *sdk.AppCtx) (*sdk.Caller, string, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AgentID <= 0 || strings.TrimSpace(caller.ThreadID) == "" {
		return nil, "", errors.New("trusted agent thread context required")
	}
	projectID := strings.TrimSpace(caller.ProjectID)
	if projectID == "" && app != nil {
		projectID = strings.TrimSpace(app.CurrentProject())
	}
	if projectID == "" {
		return nil, "", errors.New("trusted project context required")
	}
	return caller, projectID, nil
}

func decodeSchedule(v any) (*ScheduleInput, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var input ScheduleInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	// Some structured-output providers materialize every optional nested
	// property. An omitted schedule can therefore arrive as
	// {kind:"once", at:"", after:"", ...}. It carries no scheduling intent
	// and must retain the tool contract's immediate-task default.
	if strings.TrimSpace(input.At) == "" && strings.TrimSpace(input.After) == "" &&
		strings.TrimSpace(input.Every) == "" && strings.TrimSpace(input.Cron) == "" {
		return nil, nil
	}
	return &input, nil
}

func (a *App) taskForCaller(ctx context.Context, app *sdk.AppCtx, args map[string]any) (*sdk.Caller, *Task, error) {
	caller, projectID, err := callIdentity(ctx, app)
	if err != nil {
		return nil, nil, err
	}
	task, err := a.store.Get(stringArg(args, "task_id"))
	if err != nil {
		return nil, nil, err
	}
	if task.AgentID != caller.AgentID || task.ProjectID != projectID {
		return nil, nil, errors.New("task is outside the calling agent and project")
	}
	return caller, task, nil
}

func (a *App) toolCreate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, projectID, err := callIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	title := stringArg(args, "title")
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("title required")
	}
	schedule, err := decodeSchedule(args["schedule"])
	if err != nil {
		return nil, err
	}
	assigned := strings.TrimSpace(stringArg(args, "assigned_thread_id"))
	if assigned == "" && schedule != nil {
		agent, agentErr := app.GetAgent(caller.AgentID)
		if agentErr != nil {
			return nil, fmt.Errorf("resolve scheduled assignee: %w", agentErr)
		}
		assigned = strings.TrimSpace(agent.DefaultThreadID)
	}
	if assigned == "" {
		assigned = caller.ThreadID
	}
	task, created, err := a.store.Create(CreateTaskInput{AgentID: caller.AgentID, ProjectID: projectID, Title: title, Description: stringArg(args, "description"), State: stateQueued, CreatedByThreadID: caller.ThreadID, AssignedThreadID: assigned, IdempotencyKey: stringArg(args, "idempotency_key"), Schedule: schedule})
	if err != nil {
		return nil, err
	}
	if created && schedule == nil && assigned != caller.ThreadID {
		_ = a.notifyAssigned(task.ID, "task.assigned")
	}
	return map[string]any{"task": task, "created": created}, nil
}

func (a *App) toolList(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, projectID, err := callIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	filter := TaskFilter{ProjectID: projectID, AgentID: caller.AgentID, Limit: intArg(args, "limit")}
	filter.States = stringSliceArg(args, "states")
	includeRuns := boolArg(args, "include_runs")
	if !includeRuns {
		empty := ""
		filter.ParentTaskID = &empty
	}
	tasks, err := a.store.List(filter)
	if err != nil {
		return nil, err
	}
	counts, _ := a.store.Counts(projectID, caller.AgentID, includeRuns)
	return map[string]any{"tasks": tasks, "counts": counts}, nil
}

func (a *App) toolGet(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	events, err := a.store.Events(task.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task, "events": events}, nil
}

func (a *App) toolUpdate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	if schedule, scheduleErr := decodeSchedule(args["schedule"]); scheduleErr != nil {
		return nil, scheduleErr
	} else if schedule != nil {
		return a.store.SetSchedule(task.ID, caller.ThreadID, *schedule)
	}
	input := UpdateTaskInput{}
	if v, ok := args["state"].(string); ok && v != "" {
		input.State = &v
	}
	if v, ok := optionalIntArg(args, "progress"); ok {
		input.Progress = &v
	}
	if v, ok := args["current_step"].(string); ok {
		input.CurrentStep = &v
	}
	if v, ok := args["error"].(string); ok {
		input.Error = &v
	}
	if input.State != nil && strings.EqualFold(strings.TrimSpace(*input.State), stateRunning) && strings.TrimSpace(task.ExecutionThreadID) == "" {
		executionThreadID := strings.TrimSpace(task.AssignedThreadID)
		if executionThreadID == "" {
			executionThreadID = caller.ThreadID
		}
		input.ExecutionThreadID = &executionThreadID
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, input)
	if err != nil {
		return nil, err
	}
	if terminalState(updated.State) {
		_ = a.notifyCreator(updated)
	}
	return updated, nil
}

func (a *App) toolAssign(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(stringArg(args, "thread_id"))
	if target == "" {
		return nil, errors.New("thread_id required")
	}
	input := UpdateTaskInput{AssignedThreadID: &target}
	if target != task.AssignedThreadID {
		noExecutor := ""
		input.ExecutionThreadID = &noExecutor
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, input)
	if err != nil {
		return nil, err
	}
	if target != caller.ThreadID {
		_ = a.notifyAssigned(task.ID, "task.assigned")
	}
	return updated, nil
}

func (a *App) toolComplete(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	state, result := stateCompleted, strings.TrimSpace(stringArg(args, "result"))
	if result == "" {
		return nil, errors.New("result required")
	}
	input := UpdateTaskInput{State: &state, Result: &result}
	if strings.TrimSpace(task.ExecutionThreadID) == "" {
		executionThreadID := strings.TrimSpace(task.AssignedThreadID)
		if executionThreadID == "" {
			executionThreadID = caller.ThreadID
		}
		input.ExecutionThreadID = &executionThreadID
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, input)
	if err != nil {
		return nil, err
	}
	_ = a.notifyCreator(updated)
	return updated, nil
}

func (a *App) toolCancel(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	state, reason := stateCancelled, strings.TrimSpace(stringArg(args, "reason"))
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, UpdateTaskInput{State: &state, Error: &reason})
	if err != nil {
		return nil, err
	}
	if task.ScheduleKind != "" {
		_, _ = a.store.db.Exec(`UPDATE tasks SET schedule_enabled=0, next_run_at=NULL WHERE id=?`, task.ID)
	}
	_ = a.notifyCreator(updated)
	return updated, nil
}

func (a *App) toolPause(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	return a.store.Pause(task.ID, caller.ThreadID)
}
func (a *App) toolResume(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	return a.store.Resume(task.ID, caller.ThreadID)
}
func (a *App) toolRunNow(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	return a.store.RunNow(task.ID, caller.ThreadID)
}

func stringArg(args map[string]any, key string) string { v, _ := args[key].(string); return v }
func boolArg(args map[string]any, key string) bool     { v, _ := args[key].(bool); return v }
func intArg(args map[string]any, key string) int       { v, _ := optionalIntArg(args, key); return v }
func optionalIntArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, e := v.Int64()
		return int(n), e == nil
	}
	return 0, false
}
func stringSliceArg(args map[string]any, key string) []string {
	raw, _ := args[key].([]any)
	out := []string{}
	for _, item := range raw {
		if v, ok := item.(string); ok && strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	if direct, ok := args[key].([]string); ok {
		return direct
	}
	return out
}
