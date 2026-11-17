package main

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// Status changes were one MCP tool per target state. Agents now see one tool
// per object with a status argument; each call still runs the same internal
// action (activate, assignment_pause, trigger_activate, ...) as the HTTP API,
// so validation, locking and events are unchanged.
var mergedStatusTools = map[string]bool{
	"activate": true, "pause": true, "draft": true, "archive": true,
	"assignment_get": true, "assignment_activate": true, "assignment_pause": true, "assignment_archive": true,
	"trigger_activate": true, "trigger_pause": true,
}

var statusActions = map[string]map[string]string{
	"status":            {"active": "activate", "paused": "pause", "draft": "draft", "archived": "archive"},
	"assignment_status": {"active": "assignment_activate", "paused": "assignment_pause", "archived": "assignment_archive"},
	"trigger_status":    {"active": "trigger_activate", "paused": "trigger_pause"},
}

// statusAction resolves a consolidated tool call to its internal action.
func statusAction(tool string, args map[string]any) (string, map[string]any, error) {
	if tool == "assignments" {
		if str(args, "assignment_id") != "" {
			return "assignment_get", args, nil
		}
		return "assignments", args, nil
	}
	actions, ok := statusActions[tool]
	if !ok {
		return tool, args, nil
	}
	status := str(args, "status")
	action, ok := actions[status]
	if !ok {
		return "", nil, fmt.Errorf("status must be one of %s", enumList(actions))
	}
	rest := make(map[string]any, len(args))
	for k, v := range args {
		if k != "status" {
			rest[k] = v
		}
	}
	return action, rest, nil
}

func enumList(actions map[string]string) string {
	out := ""
	for _, state := range []string{"active", "paused", "draft", "archived"} {
		if _, ok := actions[state]; ok {
			if out != "" {
				out += ", "
			}
			out += state
		}
	}
	return out
}

func (a *App) consolidatedHandler(tool string) func(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
		caller := sdk.CallerFrom(ctx)
		if caller == nil || caller.AgentID <= 0 || caller.ProjectID == "" {
			return nil, errors.New("trusted agent and project context required")
		}
		if fixed := a.ctx.CurrentProject(); fixed != "" && fixed != caller.ProjectID {
			return nil, errors.New("project scope mismatch")
		}
		action, rest, err := statusAction(tool, args)
		if err != nil {
			return nil, err
		}
		return a.executeMCP(caller.ProjectID, fmt.Sprintf("agent:%d:%s", caller.AgentID, caller.ThreadID), action, rest)
	}
}

func consolidatedTools(a *App, tools []sdk.Tool) []sdk.Tool {
	out := make([]sdk.Tool, 0, len(tools))
	for _, tool := range tools {
		if mergedStatusTools[tool.Name] {
			continue
		}
		if tool.Name == "assignments" {
			tool.Description = "List saved process assignments, or read one assignment's settings with assignment_id. Assignments hold separate owners, parameters, schedules and execution modes."
			tool.InputSchema = object([]string{"process_id"}, map[string]any{"process_id": textField("Process ID"), "assignment_id": textField("Exact assignment ID to read one assignment's settings")})
			tool.HandlerCtx = a.consolidatedHandler("assignments")
		}
		out = append(out, tool)
	}
	revision := map[string]any{"type": "integer", "minimum": 1}
	return append(out,
		sdk.Tool{Name: "status", Description: "Change a procedure's status. active: only with explicit user authorization, after review; an unassigned procedure schedules no work. paused: stop future scheduled runs without interrupting current work. draft: return to draft for editing; stops new runs, existing runs continue. archived: retire it. Check sync_pending before claiming success.",
			InputSchema: object([]string{"process_id", "status"}, map[string]any{"process_id": textField("Process ID"), "status": map[string]any{"type": "string", "enum": []string{"active", "paused", "draft", "archived"}}}),
			HandlerCtx:  a.consolidatedHandler("status")},
		sdk.Tool{Name: "assignment_status", Description: "Change one assignment's status. active: only with explicit user authorization and only after the reviewed process is active. paused: stop its future runs. archived: retire it.",
			InputSchema: object([]string{"process_id", "assignment_id", "status"}, map[string]any{"process_id": textField("Process ID"), "assignment_id": textField("Assignment ID"), "status": map[string]any{"type": "string", "enum": []string{"active", "paused", "archived"}}}),
			HandlerCtx:  a.consolidatedHandler("assignment_status")},
		sdk.Tool{Name: "trigger_status", Description: "Activate or pause an event trigger. Preview sample events before activating; check sync_pending before claiming the listener is live.",
			InputSchema: object([]string{"process_id", "trigger_id", "status", "expected_revision"}, map[string]any{"process_id": textField("Process ID"), "trigger_id": textField("Trigger ID"), "status": map[string]any{"type": "string", "enum": []string{"active", "paused"}}, "expected_revision": revision}),
			HandlerCtx:  a.consolidatedHandler("trigger_status")},
	)
}
