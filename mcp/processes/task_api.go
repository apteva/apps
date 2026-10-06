package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"strings"
)

func (a *App) taskTools() []sdk.Tool {
	executor := object([]string{"kind"}, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"agent", "human"}}, "agent_id": map[string]any{"type": "integer", "minimum": 1}})
	settings := map[string]any{"title": textField("Task title"), "instructions": textField("What needs to be done"), "expected_output": textField("Result evidence needed for completion"), "executor": executor, "due_at": textField("Optional RFC3339 due date; empty clears it")}
	out := []sdk.Tool{}
	for _, name := range []string{"tasks", "task_runs", "task_create", "task_get", "task_update", "task_cancel"} {
		name := name
		props := map[string]any{}
		required := []string{}
		desc := "Manage native Processes tasks without the Tasks app."
		switch name {
		case "tasks":
			desc = "Find project work across procedure tasks, standalone tasks and tasks added to runs. Defaults to your assigned tasks for agents; use assignee=all for project work."
			for _, k := range []string{"assignee", "state", "origin", "process_id", "run_id", "search"} {
				props[k] = textField(k)
			}
			props["overdue"] = map[string]any{"type": "boolean"}
			props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
			props["offset"] = map[string]any{"type": "integer", "minimum": 0}
		case "task_runs":
			desc = "List active project runs to which the coordinator or operator can add tasks."
		case "task_create":
			for k, v := range settings {
				props[k] = v
			}
			props["run_id"] = textField("Optional active run ID; omit for standalone work")
			props["required"] = map[string]any{"type": "boolean", "description": "Whether an attached task gates completion of its run"}
			props["kind"] = map[string]any{"type": "string", "enum": []string{"work", "approval"}}
			props["depends_on"] = map[string]any{"type": "array", "items": textField("Existing task key in the attached run")}
			props["idempotency_key"] = textField("Stable creation key; reuse with identical inputs on retry")
			required = []string{"title", "instructions", "executor", "idempotency_key"}
			desc = "Create one-off work, optionally attached to an active run. Ready agent work dispatches immediately. Only the coordinator or operator can add work to a run. No procedure is created."
		case "task_get":
			desc = "Read task instructions, current revision, dependency outputs, run context and audit history before acting."
		case "task_update":
			for k, v := range settings {
				props[k] = v
			}
			for _, k := range []string{"state", "output", "error", "decision"} {
				props[k] = textField(k)
			}
			props["progress"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 100}
			desc = "Update your assigned task using its current revision. Completion needs output evidence; approval needs an explicit decision. Creator/coordinator/operator may edit settings; reassignment or text changes are allowed only before delivery attempts. Change settings and outcome in separate requests."
		case "task_cancel":
			props["reason"] = textField("Cancellation reason; already dispatched actions cannot be revoked")
			required = append(required, "reason")
		}
		if name == "task_get" || name == "task_update" || name == "task_cancel" {
			props["task_id"] = textField("Native Processes task ID (also accepts existing step IDs)")
			required = append(required, "task_id")
		}
		if name == "task_update" || name == "task_cancel" {
			props["expected_revision"] = map[string]any{"type": "integer", "minimum": 1}
			required = append(required, "expected_revision")
		}
		out = append(out, sdk.Tool{Name: name, Description: desc, InputSchema: object(required, props), HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			c := sdk.CallerFrom(ctx)
			if c == nil || c.AgentID <= 0 || c.ProjectID == "" {
				return nil, errors.New("trusted agent and project required")
			}
			if fixed := a.ctx.CurrentProject(); fixed != "" && fixed != c.ProjectID {
				return nil, errors.New("project scope mismatch")
			}
			return a.execute(c.ProjectID, fmt.Sprintf("agent:%d:%s", c.AgentID, c.ThreadID), name, args)
		}})
	}
	return out
}
func (a *App) executeTask(project, actor, action string, args map[string]any) (any, error) {
	switch action {
	case "tasks":
		return a.listNativeTasks(project, actor, args)
	case "task_runs":
		return a.nativeTaskRuns(project)
	case "task_create":
		var c TaskConfig
		if e := decodeTaskValue(args, &c); e != nil {
			return nil, e
		}
		s, e := a.createTask(project, actor, str(args, "idempotency_key"), c)
		if e != nil {
			return nil, e
		}
		return a.taskDetails(project, actor, s.ID)
	case "task_get":
		return a.taskDetails(project, actor, str(args, "task_id"))
	case "task_update", "task_cancel":
		return a.changeTask(project, actor, str(args, "task_id"), action, args)
	}
	return nil, errors.New("unknown task action")
}
func (a *App) listNativeTasks(project, actor string, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	values := []any{project}
	for _, k := range []string{"state", "origin", "run_id"} {
		if v := str(args, k); v != "" {
			if k == "state" && v == "active" {
				where = append(where, `state NOT IN ('completed','failed','cancelled')`)
				continue
			}
			where = append(where, k+"=?")
			values = append(values, v)
		}
	}
	assignee := str(args, "assignee")
	if assignee == "" && strings.HasPrefix(actor, "agent:") {
		assignee = strings.SplitN(actor, ":", 3)[1]
	}
	if assignee != "" && assignee != "all" {
		if assignee == "human" {
			where = append(where, `json_extract(executor_json,'$.kind')='human'`)
		} else {
			id, e := strconv.Atoi(assignee)
			if e != nil || id < 1 {
				return nil, errors.New("assignee must be an agent ID, human, or all")
			}
			where = append(where, `json_extract(executor_json,'$.kind')='agent' AND json_extract(executor_json,'$.agent_id')=?`)
			values = append(values, id)
		}
	}
	if v := str(args, "process_id"); v != "" {
		where = append(where, `run_id IN (SELECT id FROM process_runs WHERE process_id=?)`)
		values = append(values, v)
	}
	if v := str(args, "search"); v != "" {
		where = append(where, `(json_extract(definition_json,'$.name') LIKE ? OR json_extract(definition_json,'$.instructions') LIKE ?)`)
		values = append(values, "%"+v+"%", "%"+v+"%")
	}
	if args["overdue"] == true {
		where = append(where, `due_at<>'' AND julianday(due_at)<julianday(?) AND state NOT IN ('completed','failed','cancelled')`)
		values = append(values, timestamp())
	}
	limit := number(args, "limit")
	if limit == 0 {
		limit = 50
	}
	offset := number(args, "offset")
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, errors.New("invalid pagination")
	}
	clause := strings.Join(where, " AND ")
	var total int
	if e := a.db.QueryRow(`SELECT count(*) FROM process_step_runs WHERE `+clause, values...).Scan(&total); e != nil {
		return nil, e
	}
	values = append(values, limit, offset)
	rows, e := a.db.Query(`SELECT `+stepColumns+` FROM process_step_runs WHERE `+clause+` ORDER BY CASE WHEN due_at='' THEN 1 ELSE 0 END,julianday(due_at),created_at DESC,id LIMIT ? OFFSET ?`, values...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		s, e := scanStep(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return map[string]any{"tasks": out, "total": total, "offset": offset, "limit": limit}, rows.Err()
}
func (a *App) nativeTaskRuns(project string) (any, error) {
	rows, e := a.db.Query(`SELECT r.id,p.id,p.name,r.state FROM process_runs r JOIN processes p ON p.id=r.process_id WHERE p.project_id=? AND (r.backend='agent' OR r.workflow=1) AND r.state NOT IN ('completed','failed','cancelled') ORDER BY r.created_at DESC LIMIT 200`, project)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var id, process, name, state string
		if e = rows.Scan(&id, &process, &name, &state); e != nil {
			return nil, e
		}
		runs = append(runs, map[string]any{"id": id, "process_id": process, "process_name": name, "state": state})
	}
	return map[string]any{"runs": runs}, rows.Err()
}
