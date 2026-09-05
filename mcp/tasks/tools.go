package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": false}
}

func scheduleSchema(requireKind bool) map[string]any {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"kind":           map[string]any{"type": "string", "enum": []string{"once", "interval", "cron"}},
		"at":             map[string]any{"type": "string", "description": "RFC3339 timestamp for a one-time schedule."},
		"after":          map[string]any{"type": "string", "description": "Relative duration such as 10m; server time is authoritative."},
		"every":          map[string]any{"type": "string", "description": "Interval such as 1h or 24h."},
		"cron":           map[string]any{"type": "string", "description": "Five-field cron expression."},
		"timezone":       map[string]any{"type": "string", "description": "IANA timezone, default UTC."},
		"overlap_policy": map[string]any{"type": "string", "enum": []string{"skip"}},
		"catchup_policy": map[string]any{"type": "string", "enum": []string{"skip"}},
	}}
	if requireKind {
		schema["required"] = []string{"kind"}
	}
	return schema
}

func (a *App) tools() []sdk.Tool {
	wakeAlways := map[string]any{"io.apteva/wakeOnResult": "always"}
	return []sdk.Tool{
		{Name: "create", Description: "Create exactly one durable task before substantive work begins when an outcome is multi-step, combines multiple sources or independent checks, is scheduled, delegated, or must survive the current exchange. A multi-area review or synthesis is task work even when calls run in parallel or finish in one turn. A quick lookup means one bounded read or action with no multi-source synthesis. Creating a task does not imply delegation: the current opaque thread may create, execute, update, and complete its own task. For immediate work, omit schedule entirely; never invent schedule placeholders. For scheduled work, provide exactly one of at, after, every, or cron with its matching kind. The current thread becomes creator; immediate work defaults to that creator, while scheduled work defaults to the agent's configured default thread. assigned_thread_id overrides either default.", InputSchema: objectSchema([]string{"title"}, map[string]any{
			"title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "assigned_thread_id": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}, "operation_key": map[string]any{"type": "string", "description": "Stable external-operation idempotency context, when the domain API provides one."}, "schedule": scheduleSchema(true),
		}), Meta: wakeAlways, HandlerCtx: a.toolCreate},
		{Name: "list", Description: "List this agent's durable task inventory across all of its threads. Caller, creator, assignee, and executor thread IDs are provenance only and never limit visibility. Use this directly for task and schedule questions instead of asking another thread. Use next_cursor to request the next page when has_more is true; do not repeat a page.", InputSchema: objectSchema(nil, map[string]any{
			"states": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "include_runs": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"}, "cursor": map[string]any{"type": "string"}, "view": map[string]any{"type": "string", "enum": []string{"all", "active", "attention", "scheduled", "recent"}}, "query": map[string]any{"type": "string"},
		}), HandlerCtx: a.toolList},
		{Name: "get", Description: "Read the authoritative task and its chronological event history. Every thread receiving a task event, including a delegated worker, must call this before any domain action. Main may grant this read-only tool to a worker without granting Tasks mutation tools.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "events_cursor": map[string]any{"type": "string"}}), HandlerCtx: a.toolGet},
		{Name: "set_progress", Description: "Record one meaningful nonterminal task milestone. Always provide state, progress, and a concrete current_step. State must be running while any executor is actively working, waiting only when nobody can progress until an external event, or blocked for a concrete dependency. Progress is monotonic and must not decrease. Progress 100 is still nonterminal: call tasks_complete after success or tasks_fail after failure. Do not call after every tool call.", InputSchema: objectSchema([]string{"task_id", "state", "progress", "current_step"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "state": map[string]any{"type": "string", "enum": []string{"running", "waiting", "blocked"}}, "progress": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "current_step": map[string]any{"type": "string"},
		}), Meta: wakeAlways, HandlerCtx: a.toolSetProgress},
		{Name: "edit", Description: "Atomically edit an existing task definition while preserving omitted fields. Change title, description, and/or a partial schedule. Use clear_description=true to intentionally remove the description. For recurring work, edit the recurring parent; dispatched or materialized occurrence definitions are immutable. Editing a paused schedule keeps it paused. Call tasks_get first and never create a placeholder replacement task.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "clear_description": map[string]any{"type": "boolean"}, "schedule": scheduleSchema(false),
		}), Meta: wakeAlways, HandlerCtx: a.toolEdit},
		{Name: "fail", Description: "Record an explicit terminal task failure with a concrete error. Use this instead of tasks_update(state=failed). Include result_reference when durable evidence or an external operation record exists. Call exactly once before pace, idle, stopping, or sending the final response.", InputSchema: objectSchema([]string{"task_id", "error"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}, "result_reference": map[string]any{"type": "string"},
		}), Meta: wakeAlways, HandlerCtx: a.toolFail},
		{Name: "update", Description: "Legacy compatibility tool combining definition edits, progress, and failure state. Do not use for new work. Use tasks_set_progress for milestones, tasks_edit for definitions, tasks_fail for failures, and tasks_complete for success.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "state": map[string]any{"type": "string", "enum": []string{"queued", "running", "waiting", "blocked", "failed"}}, "progress": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "current_step": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}, "result_reference": map[string]any{"type": "string"}, "schedule": scheduleSchema(false),
		}), Meta: wakeAlways, HandlerCtx: a.toolUpdate},
		{Name: "assign", Description: "Assign a task to an existing opaque thread id belonging to the same agent. This records ownership and sends an event containing the authoritative task ID, which wakes a paused worker; it never creates a thread. Main must use the platform spawn tool first, grant the worker tasks_get plus only required domain tools, and retain all Tasks mutation tools. The worker must call tasks_get before any domain action.", InputSchema: objectSchema([]string{"task_id", "thread_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "thread_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolAssign},
		{Name: "complete", Description: "Required final write after successful task work. Call tasks_complete exactly once with the concrete result before pace, idle, stopping, or sending the final response. Neither progress 100 nor a final-looking current_step completes the task. Completion sets progress to 100 and current_step to Completed. When a worker reports success, main must record the result here before it stops. Include result_reference when a durable output ID or URL exists. Tasks sends a structured terminal receipt to its creator thread when different; do not duplicate that delivery.", InputSchema: objectSchema([]string{"task_id", "result"}, map[string]any{"task_id": map[string]any{"type": "string"}, "result": map[string]any{"type": "string"}, "result_reference": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolComplete},
		{Name: "cancel", Description: "Cancel an active task or scheduled series.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolCancel},
		{Name: "pause", Description: "Pause a scheduled task without deleting it.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolPause},
		{Name: "resume", Description: "Resume a paused scheduled task, preserving a future deadline and skipping overdue recurring runs.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolResume},
		{Name: "run_now", Description: "Run one manual occurrence now, preserving the recurring schedule cadence and pause state. Returns the occurrence to execute; do not use this to recover a failed occurrence.", InputSchema: objectSchema([]string{"task_id"}, map[string]any{"task_id": map[string]any{"type": "string"}}), Meta: wakeAlways, HandlerCtx: a.toolRunNow},
		{Name: "recover_occurrence", Description: "Create one linked reconciliation-only attempt for a failed scheduled occurrence. This does not reopen the original occurrence and must not blindly repeat its external action. The recovery executor must call tasks_get first, reconcile durable external state, complete when the original outcome is verified, retry the domain action only when it is proven absent and safe, or record failed with external_outcome_unknown. Use this instead of run_now when recovering a specific occurrence.", InputSchema: objectSchema([]string{"task_id", "reason"}, map[string]any{
			"task_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"},
		}), Meta: wakeAlways, HandlerCtx: a.toolRecoverOccurrence},
	}
}

func callIdentity(ctx context.Context, app *sdk.AppCtx) (*sdk.Caller, string, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AgentID <= 0 || strings.TrimSpace(caller.ThreadID) == "" {
		return nil, "", validationError("trusted agent thread context required")
	}
	projectID := strings.TrimSpace(caller.ProjectID)
	if projectID == "" && app != nil {
		projectID = strings.TrimSpace(app.CurrentProject())
	}
	if projectID == "" {
		return nil, "", validationError("trusted project context required")
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

func decodeSchedulePatch(v any) (*ScheduleInput, error) {
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
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.At = strings.TrimSpace(input.At)
	input.After = strings.TrimSpace(input.After)
	input.Every = strings.TrimSpace(input.Every)
	input.Cron = strings.TrimSpace(input.Cron)
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.OverlapPolicy = strings.TrimSpace(input.OverlapPolicy)
	input.CatchupPolicy = strings.TrimSpace(input.CatchupPolicy)
	if input.Kind == "" && input.At == "" && input.After == "" && input.Every == "" &&
		input.Cron == "" && input.Timezone == "" && input.OverlapPolicy == "" && input.CatchupPolicy == "" {
		return nil, nil
	}
	// This is the placeholder emitted by structured providers for an omitted
	// schedule object. A real UTC-only edit can include the current expression.
	defaultOverlap := input.OverlapPolicy == "" || strings.EqualFold(input.OverlapPolicy, "skip")
	defaultCatchup := input.CatchupPolicy == "" || strings.EqualFold(input.CatchupPolicy, "skip")
	if input.Kind == scheduleOnce && input.At == "" && input.After == "" && input.Every == "" &&
		input.Cron == "" && (input.Timezone == "" || input.Timezone == "UTC") &&
		defaultOverlap && defaultCatchup {
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
		return nil, nil, validationError("task is outside the calling agent and project")
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
		return nil, validationError("title required")
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
	task, created, err := a.store.Create(CreateTaskInput{AgentID: caller.AgentID, ProjectID: projectID, Title: title, Description: stringArg(args, "description"), State: stateQueued, CreatedByThreadID: caller.ThreadID, AssignedThreadID: assigned, IdempotencyKey: stringArg(args, "idempotency_key"), OperationKey: stringArg(args, "operation_key"), Schedule: schedule})
	if err != nil {
		return nil, err
	}
	if created && schedule == nil && assigned != caller.ThreadID {
		if err := a.notifyAssigned(task, assigned, "task.assigned"); err != nil {
			return nil, err
		}
	}
	return map[string]any{"task": task, "created": created}, nil
}

func (a *App) toolList(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, projectID, err := callIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	filter := TaskFilter{ProjectID: projectID, AgentID: caller.AgentID, Limit: intArg(args, "limit"), Cursor: stringArg(args, "cursor"), View: stringArg(args, "view"), Search: stringArg(args, "query")}
	filter.States = stringSliceArg(args, "states")
	includeRuns := boolArg(args, "include_runs")
	if !includeRuns {
		empty := ""
		filter.ParentTaskID = &empty
	}
	page, err := a.store.ListPage(filter)
	if err != nil {
		return nil, err
	}
	counts, err := a.store.Counts(projectID, caller.AgentID, includeRuns)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tasks": page.Tasks, "next_cursor": page.NextCursor, "has_more": page.HasMore, "counts": counts}, nil
}

func (a *App) toolGet(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	if accepted, changed, acceptErr := a.store.Accept(task.ID, caller.ThreadID, nowUTC()); acceptErr != nil {
		return nil, acceptErr
	} else if changed {
		task = accepted
	}
	events, err := a.store.EventsPage(task.ID, stringArg(args, "events_cursor"), 100)
	if err != nil {
		return nil, err
	}
	executions, err := a.store.AgentExecutions(task.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task, "events": events.Events, "events_next_cursor": events.NextCursor, "agent_executions": executions}, nil
}

func (a *App) toolSetProgress(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	state := strings.ToLower(strings.TrimSpace(stringArg(args, "state")))
	if state != stateRunning && state != stateWaiting && state != stateBlocked {
		return nil, validationError("state must be running, waiting, or blocked")
	}
	progress, ok := optionalIntArg(args, "progress")
	if !ok {
		return nil, validationError("progress required")
	}
	if progress < 0 || progress > 100 {
		return nil, errInvalidProgress
	}
	if task.Progress != nil && progress < *task.Progress {
		return nil, validationError("progress cannot decrease")
	}
	step := strings.TrimSpace(stringArg(args, "current_step"))
	if step == "" {
		return nil, validationError("current_step required")
	}
	input := UpdateTaskInput{State: &state, Progress: &progress, CurrentStep: &step}
	if state == stateRunning && strings.TrimSpace(task.ExecutionThreadID) == "" {
		executionThreadID := strings.TrimSpace(task.AssignedThreadID)
		if executionThreadID == "" {
			executionThreadID = caller.ThreadID
		}
		input.ExecutionThreadID = &executionThreadID
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, input)
	return updated, err
}

func (a *App) toolEdit(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	if terminalState(task.State) {
		return nil, errTerminalTask
	}
	if task.ParentTaskID != "" || task.ScheduledFor != nil {
		return nil, validationError("occurrence definitions are immutable; edit the recurring parent task")
	}
	input := UpdateTaskInput{}
	if title := strings.TrimSpace(stringArg(args, "title")); title != "" {
		input.Title = &title
	}
	clearDescription := boolArg(args, "clear_description")
	if clearDescription {
		description := ""
		input.Description = &description
	} else if description := stringArg(args, "description"); description != "" {
		input.Description = &description
	}
	schedule, err := decodeSchedulePatch(args["schedule"])
	if err != nil {
		return nil, err
	}
	// Structured-output providers can fill an omitted optional kind with the
	// enum's first value. Keep the existing recurring kind when the call has no
	// concrete one-time expression and edits another schedule field instead.
	if schedule != nil && schedule.Kind == scheduleOnce && schedule.At == "" &&
		schedule.After == "" && task.ScheduleKind != scheduleOnce {
		schedule.Kind = ""
	}
	input.Schedule = schedule
	if input.Title == nil && input.Description == nil && input.Schedule == nil {
		return nil, validationError("at least one definition field is required")
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, input)
	return updated, err
}

func (a *App) toolFail(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	failure := strings.TrimSpace(stringArg(args, "error"))
	if failure == "" {
		return nil, validationError("error required")
	}
	state, step := stateFailed, "Failed"
	input := UpdateTaskInput{State: &state, Error: &failure, CurrentStep: &step}
	if reference := strings.TrimSpace(stringArg(args, "result_reference")); reference != "" {
		input.ResultReference = &reference
	}
	updated, _, err := a.store.Update(task.ID, caller.ThreadID, input)
	if err != nil {
		return nil, err
	}
	_ = a.notifyCreator(updated)
	return updated, nil
}

func (a *App) toolUpdate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	schedule, scheduleErr := decodeSchedulePatch(args["schedule"])
	if scheduleErr != nil {
		return nil, scheduleErr
	}
	input := UpdateTaskInput{}
	// Structured-output providers may materialize every optional string as "".
	// Treat those placeholders as omitted so a progress-only update cannot erase
	// the authoritative task body or other durable metadata. HTTP PATCH/PUT still
	// supports explicit empty-string updates because it bypasses this MCP adapter.
	if v, ok := args["title"].(string); ok && v != "" {
		input.Title = &v
	}
	if v, ok := args["description"].(string); ok && v != "" {
		input.Description = &v
	}
	if v, ok := args["state"].(string); ok && v != "" {
		input.State = &v
	}
	if v, ok := optionalIntArg(args, "progress"); ok {
		input.Progress = &v
	}
	if v, ok := args["current_step"].(string); ok && v != "" {
		input.CurrentStep = &v
	}
	if v, ok := args["error"].(string); ok && v != "" {
		input.Error = &v
	}
	if v, ok := args["result_reference"].(string); ok && v != "" {
		input.ResultReference = &v
	}
	if schedule != nil {
		// Some structured providers fill an omitted optional enum with its first
		// value. A partial timezone/policy edit has no once expression, so that
		// synthetic kind must not replace an existing recurring kind.
		if schedule.Kind == scheduleOnce && schedule.At == "" && schedule.After == "" && task.ScheduleKind != scheduleOnce {
			schedule.Kind = ""
		}
		input.Schedule = schedule
	}
	definitionEdit := input.Title != nil || input.Description != nil || input.Schedule != nil
	if definitionEdit && input.State != nil && input.Progress != nil && *input.Progress == 0 &&
		strings.TrimSpace(stringArg(args, "current_step")) == "" && strings.TrimSpace(stringArg(args, "error")) == "" {
		// Codex structured output materializes omitted execution fields as this
		// tuple during a definition-only edit, with an arbitrary enum value for
		// state. Preserve the lifecycle; an intentional state change must include
		// its concrete current_step or error.
		input.State = nil
		input.Progress = nil
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
		return nil, validationError("thread_id required")
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
		if err := a.notifyAssigned(updated, target, "task.assigned"); err != nil {
			return nil, err
		}
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
		return nil, validationError("result required")
	}
	input := UpdateTaskInput{State: &state, Result: &result}
	if reference := strings.TrimSpace(stringArg(args, "result_reference")); reference != "" {
		input.ResultReference = &reference
	}
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
	run, err := a.store.RunNow(task.ID, caller.ThreadID)
	if err != nil {
		return nil, err
	}
	_ = a.drainDeliveries(run.ID, run.ProjectID, a.store.now())
	return run, nil
}

func (a *App) toolRecoverOccurrence(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, task, err := a.taskForCaller(ctx, app, args)
	if err != nil {
		return nil, err
	}
	recovery, created, err := a.store.RecoverOccurrence(task.ID, caller.ThreadID, caller.ThreadID,
		stringArg(args, "reason"), stringArg(args, "idempotency_key"), nowUTC())
	if err != nil {
		return nil, err
	}
	if created {
		recovery, _, err = a.store.MarkDispatched(recovery.ID, "tasks:recovery", nowUTC())
		if err != nil {
			return nil, err
		}
		if err := a.notifyAssigned(recovery, recovery.AssignedThreadID, "task.recovery.ready"); err != nil {
			return nil, fmt.Errorf("recovery created and queued for durable redispatch: %w", err)
		}
	}
	return map[string]any{"task": recovery, "created": created, "recovery_of_task_id": recovery.RecoveryOfTaskID}, nil
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
