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
		{Name: "create", Description: "Create exactly one durable task for multi-step, scheduled, delegated, or leave-and-return work. Do not create tasks for brief answers or quick lookups. The current opaque thread becomes creator. Immediate work defaults to that creator; scheduled work defaults to the agent's configured default thread. assigned_thread_id overrides either default.", InputSchema: objectSchema([]string{"title"}, map[string]any{
			"title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "assigned_thread_id": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}, "schedule": scheduleSchema(),
		}), Meta: wakeAlways, HandlerCtx: a.toolCreate},
		{Name: "list", Description: "List the agent's durable task inventory directly. Use this for task and schedule questions instead of asking another thread. The result is authoritative; do not repeat the same list call.", InputSchema: objectSchema(nil, map[string]any{
			"states": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "assigned_thread_id": map[string]any{"type": "string"}, "created_by_me": map[string]any{"type": "boolean"}, "include_runs": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"},
		}), HandlerCtx: a.toolList},
		{Name: "get", Description: "Get a task and its chronological event history. Read this before resuming assigned work.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), HandlerCtx: a.toolGet},
		{Name: "update", Description: "Update task state, coarse progress, or current step only at meaningful milestones, waits, blockers, or failures. This is the task's progress record; do not mirror it into global status.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "state": map[string]any{"type": "string", "enum": []string{"queued", "running", "waiting", "blocked", "failed"}}, "progress": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "current_step": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}, "schedule": scheduleSchema(),
		}), Meta: wakeAlways, HandlerCtx: a.toolUpdate},
		{Name: "assign", Description: "Assign a task to an opaque thread id belonging to the same agent. The platform does not classify the target thread.", InputSchema: objectSchema([]string{"task_id", "thread_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "thread_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolAssign},
		{Name: "spawn_thread", Description: "Create a dedicated execution thread for a task and atomically assign the task to it. Use only when separate ownership, context, waiting, or parallelism is useful.", InputSchema: objectSchema([]string{"task_id", "thread_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "thread_id": map[string]any{"type": "string"}, "instructions": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolSpawnThread},
		{Name: "complete", Description: "Complete an assigned task once with its concrete result. Tasks sends a structured terminal receipt to its creator thread when different; do not duplicate that delivery.", InputSchema: objectSchema([]string{"task_id", "result"}, map[string]any{"task_id": map[string]any{"type": "string"}, "result": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolComplete},
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
	filter := TaskFilter{ProjectID: projectID, AgentID: caller.AgentID, AssignedThread: stringArg(args, "assigned_thread_id"), Limit: intArg(args, "limit")}
	if boolArg(args, "created_by_me") {
		filter.CreatedByThread = caller.ThreadID
	}
	filter.States = stringSliceArg(args, "states")
	if !boolArg(args, "include_runs") {
		empty := ""
		filter.ParentTaskID = &empty
	}
	tasks, err := a.store.List(filter)
	if err != nil {
		return nil, err
	}
	counts, _ := a.store.Counts(projectID)
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
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, UpdateTaskInput{AssignedThreadID: &target})
	if err != nil {
		return nil, err
	}
	if target != caller.ThreadID {
		_ = a.notifyAssigned(task.ID, "task.assigned")
	}
	return updated, nil
}

func (a *App) toolSpawnThread(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	threadID := strings.TrimSpace(stringArg(args, "thread_id"))
	if threadID == "" {
		return nil, errors.New("thread_id required")
	}
	if app.ThreadAPI() == nil {
		return nil, errors.New("platform thread API unavailable")
	}
	directive := fmt.Sprintf("\n\n[ASSIGNED TASK]\nTask ID: %s\nOutcome: %s\n%s\nRead it with the Tasks app, update meaningful progress, and complete it exactly once.", task.ID, task.Title, stringArg(args, "instructions"))
	spawned, err := app.ThreadAPI().SpawnThread(sdk.ThreadSpawnRequest{AgentID: caller.AgentID, ThreadID: threadID, DirectiveSuffix: directive})
	if err != nil {
		return nil, err
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, UpdateTaskInput{AssignedThreadID: &threadID, ExecutionThreadID: &threadID})
	if err != nil {
		return nil, err
	}
	_ = a.notifyAssigned(task.ID, "task.assigned")
	return map[string]any{"task": updated, "thread": spawned.Thread, "spawn_status": spawned.Status}, nil
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
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, UpdateTaskInput{State: &state, Result: &result})
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
