package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func str(m map[string]any, key string) string { v, _ := m[key].(string); return strings.TrimSpace(v) }
func number(m map[string]any, key string) int { n, _ := m[key].(float64); return int(n) }
func object(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func textField(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func definitionSchema() map[string]any {
	return object([]string{"name", "instructions", "completion_criteria", "owner_agent_id"}, map[string]any{
		"steps": map[string]any{"type": "array", "maxItems": 30, "items": stepSchema()}, "parameters": map[string]any{"type": "array", "maxItems": 50, "items": object([]string{"key", "type"}, map[string]any{"key": textField("Unique parameter key"), "label": textField("Human-readable label"), "type": map[string]any{"type": "string", "enum": []string{"string", "number", "boolean"}}, "required": map[string]any{"type": "boolean"}, "default": map[string]any{}, "options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}})}, "execution_mode": map[string]any{"type": "string", "enum": []string{"agent", "tasks"}, "description": "Default agent; tasks requires the optional Tasks integration"}, "name": textField("Procedure name"), "description": textField("Purpose"), "instructions": textField("Ordered steps or checklist in plain language"), "required_inputs": textField("Inputs or sources the owner must obtain"), "default_inputs": textField("Standing execution context"), "completion_criteria": textField("Required outcomes and evidence"), "approval_requirements": textField("Explicit approval checkpoints; does not enforce a software gate"), "owner_agent_id": map[string]any{"type": "integer", "minimum": 1}, "schedule": object([]string{"kind"}, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"interval", "cron"}}, "every": textField("Duration, e.g. 24h; minimum 1m"), "cron": textField("Five-field cron expression"), "timezone": textField("IANA timezone; default UTC")}),
	})
}
func (a *App) MCPTools() []sdk.Tool {
	descriptions := map[string]string{"list": "Find project procedures by search, status, or owner.", "get": "Read a procedure and immutable versions. Specify version to retrieve a historical definition.", "create": "Create a draft company procedure only when authorized to define company policy.", "update": "Replace the definition with a new immutable version. Requires a draft or fully paused process and expected_version.", "activate": "Activate a procedure and its schedule. Check sync_pending before claiming success.", "pause": "Stop future scheduled runs. Existing runs continue. Check sync_pending.", "archive": "Retire a process and stop future schedules. Existing runs continue.", "start": "Start one active procedure on its owner agent. Supply a stable idempotency_key and reuse it on retries. Track execution in the selected backend.", "runs": "Read direct runs and live Tasks history when used."}
	descriptions["run_get"] = "Read the immutable procedure and direct run before acting."
	descriptions["run_update"] = "Owner only: record direct run progress, blockers, or outcome. Completion requires evidence in result."
	for _, name := range []string{"assignments", "assignment_get", "assignment_create", "assignment_update", "assignment_activate", "assignment_pause", "assignment_archive"} {
		descriptions[name] = "Manage saved process assignments: separate owners, targets, parameters, schedules, and execution modes. Update requires a paused assignment and expected_revision. Activate only after the process is active."
	}
	descriptions["run_cancel"] = "Coordinator or operator: cancel a structured run and stop future handoffs. Already dispatched external work may continue."
	descriptions["step_get"] = "Read a step, frozen executor, parameters, and completed dependency outputs before acting."
	descriptions["step_update"] = "Assigned executor only: report step progress or output; approval steps require an explicit approved/rejected decision."
	out := []sdk.Tool{}
	for _, name := range []string{"list", "get", "create", "update", "activate", "pause", "archive", "start", "runs", "run_get", "run_update", "assignments", "assignment_get", "assignment_create", "assignment_update", "assignment_activate", "assignment_pause", "assignment_archive", "step_get", "step_update", "run_cancel"} {
		name := name
		props := map[string]any{}
		required := []string{}
		if name != "list" && name != "create" {
			props["process_id"] = textField("Process ID")
			required = append(required, "process_id")
		}
		switch name {
		case "assignment_get", "assignment_update", "assignment_activate", "assignment_pause", "assignment_archive":
			props["assignment_id"] = textField("Assignment ID")
			required = append(required, "assignment_id")
			if name == "assignment_update" {
				props["assignment"] = assignmentSchema()
				props["expected_revision"] = map[string]any{"type": "integer", "minimum": 1}
				required = append(required, "assignment", "expected_revision")
			}
		case "assignment_create":
			props["assignment"] = assignmentSchema()
			required = append(required, "assignment")
		case "list":
			props["search"] = textField("Search name and purpose")
			props["status"] = textField("draft, active, paused, or archived")
			props["owner_agent_id"] = map[string]any{"type": "integer"}
		case "get":
			props["version"] = map[string]any{"type": "integer", "minimum": 1}
		case "create", "update":
			props["definition"] = definitionSchema()
			required = append(required, "definition")
			if name == "update" {
				props["expected_version"] = map[string]any{"type": "integer", "minimum": 1}
				required = append(required, "expected_version")
			}
		case "run_cancel":
			props["run_id"] = textField("Run ID")
			props["reason"] = textField("Cancellation reason")
			required = append(required, "run_id", "reason")
		case "step_get", "step_update":
			props["run_id"] = textField("Run ID")
			props["step_id"] = textField("Step execution ID")
			required = append(required, "run_id", "step_id")
			if name == "step_update" {
				props["state"] = map[string]any{"type": "string", "enum": []string{"running", "waiting", "blocked", "completed", "failed", "cancelled"}}
				props["progress"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 100}
				props["output"] = textField("Concrete result and evidence (required on completion)")
				props["error"] = textField("Reason for waiting, block, failure or cancellation")
				props["decision"] = map[string]any{"type": "string", "enum": []string{"approved", "rejected"}}
				required = append(required, "state")
			}
		case "run_get", "run_update":
			props["run_id"] = textField("Run ID")
			required = append(required, "run_id")
			if name == "run_update" {
				props["state"] = map[string]any{"type": "string", "enum": []string{"running", "waiting", "blocked", "completed", "failed", "cancelled"}}
				props["progress"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 100}
				for _, k := range []string{"current_step", "result", "error"} {
					props[k] = textField(k)
				}
				required = append(required, "state")
			}
		case "start":
			props["assignment_id"] = textField("Assignment ID; required when there are multiple non-archived assignments")
			props["parameters"] = map[string]any{"type": "object", "description": "Run-only overrides for declared parameters"}
			props["idempotency_key"] = textField("Stable unique key for this logical execution")
			props["inputs"] = textField("Optional run-specific context")
			required = append(required, "idempotency_key")
		}
		out = append(out, sdk.Tool{Name: name, Description: descriptions[name], InputSchema: object(required, props), HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			caller := sdk.CallerFrom(ctx)
			if caller == nil || caller.AgentID <= 0 || caller.ProjectID == "" {
				return nil, errors.New("trusted agent and project context required")
			}
			if fixed := a.ctx.CurrentProject(); fixed != "" && fixed != caller.ProjectID {
				return nil, errors.New("project scope mismatch")
			}
			return a.execute(caller.ProjectID, fmt.Sprintf("agent:%d:%s", caller.AgentID, caller.ThreadID), name, args)
		}})
	}
	return append(append(out, a.triggerTools()...), a.taskTools()...)
}
func (a *App) execute(project, actor, action string, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if action == "tasks" || strings.HasPrefix(action, "task_") {
		return a.executeTask(project, actor, action, args)
	}
	if strings.HasPrefix(action, "trigger_") || action == "triggers" {
		return a.executeTrigger(project, action, args)
	}
	id := str(args, "process_id")
	switch action {
	case "list":
		items, err := a.list(project)
		if err != nil {
			return nil, err
		}
		out := []Process{}
		q := strings.ToLower(str(args, "search"))
		for _, p := range items {
			if status := str(args, "status"); status != "" && p.Status != status {
				continue
			}
			if owner := number(args, "owner_agent_id"); owner > 0 {
				found := false
				for _, x := range p.Assignments {
					if x.OwnerAgentID == int64(owner) {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			if q != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.Description), q) {
				continue
			}
			out = append(out, p)
		}
		return map[string]any{"processes": out}, nil
	case "get":
		p, err := a.get(project, id)
		if err != nil {
			return nil, err
		}
		versions, err := a.versions(id)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"process": p, "versions": versions}
		if v := number(args, "version"); v > 0 {
			d, e := a.definition(id, v)
			if e != nil {
				return nil, errNotFound
			}
			result["definition"] = d
		}
		return result, nil
	case "create", "update":
		d, err := decodeDefinition(args["definition"])
		if err != nil {
			return nil, err
		}
		if action == "create" {
			id = ""
		} else if d.ExecutionMode == "" {
			// Older clients omit this field; editing must not switch backends.
			current, e := a.get(project, id)
			if e != nil {
				return nil, e
			}
			d.ExecutionMode = current.ExecutionMode
		}
		return a.save(project, id, actor, number(args, "expected_version"), d)
	case "activate":
		return a.changeStatus(project, id, "active")
	case "pause":
		return a.changeStatus(project, id, "paused")
	case "archive":
		return a.changeStatus(project, id, "archived")
	case "start":
		assignmentID := str(args, "assignment_id")
		if assignmentID == "" {
			if _, e := a.get(project, id); e != nil {
				return nil, e
			}
			xs, e := a.assignments(id)
			if e != nil {
				return nil, e
			}
			for _, x := range xs {
				if x.Status != "archived" {
					if assignmentID != "" {
						return nil, errors.New("choose assignment_id; this process has multiple assignments")
					}
					assignmentID = x.ID
				}
			}
		}
		overrides, ok := args["parameters"].(map[string]any)
		if args["parameters"] != nil && !ok {
			return nil, errors.New("parameters must be an object")
		}
		return a.startAssignment(project, id, assignmentID, str(args, "idempotency_key"), str(args, "inputs"), overrides)
	case "assignments":
		if _, e := a.get(project, id); e != nil {
			return nil, e
		}
		xs, e := a.assignments(id)
		return map[string]any{"assignments": xs}, e
	case "assignment_get":
		return a.assignment(project, id, str(args, "assignment_id"))
	case "assignment_create", "assignment_update":
		var c AssignmentConfig
		raw, e := json.Marshal(args["assignment"])
		if e == nil {
			e = json.Unmarshal(raw, &c)
		}
		if e != nil {
			return nil, e
		}
		aid := str(args, "assignment_id")
		if action == "assignment_create" {
			aid = ""
		}
		return a.saveAssignment(project, id, aid, number(args, "expected_revision"), c)
	case "assignment_activate":
		return a.assignmentStatus(project, id, str(args, "assignment_id"), "active")
	case "assignment_pause":
		return a.assignmentStatus(project, id, str(args, "assignment_id"), "paused")
	case "assignment_archive":
		return a.assignmentStatus(project, id, str(args, "assignment_id"), "archived")
	case "run_cancel":
		return a.cancelWorkflow(project, actor, id, str(args, "run_id"), str(args, "reason"))
	case "step_get", "step_update":
		return a.stepAction(project, actor, id, str(args, "run_id"), str(args, "step_id"), action, args)
	case "runs":
		return a.runs(project, id)
	case "run_get", "run_update":
		return a.directRun(project, actor, id, str(args, "run_id"), action, args)
	default:
		return nil, errors.New("unknown action")
	}
}
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/processes", Handler: a.handleHTTP}, {Pattern: "/processes/", Handler: a.handleHTTP}}
}
func (a *App) handleHTTP(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	query := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if project == "" {
		project = query
	} else if query != "" && query != project {
		http.Error(w, "project scope mismatch", 403)
		return
	}
	if project == "" || (a.ctx.CurrentProject() != "" && a.ctx.CurrentProject() != project) {
		http.Error(w, "project context required", 403)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/processes"), "/")
	parts := strings.Split(path, "/")
	args := map[string]any{}
	action := ""
	if path == "" {
		if r.Method == "GET" {
			action = "list"
			args["search"] = r.URL.Query().Get("search")
			args["status"] = r.URL.Query().Get("status")
		} else if r.Method == "POST" {
			action = "create"
		}
	} else if len(parts) == 1 {
		args["process_id"] = parts[0]
		if r.Method == "GET" {
			action = "get"
			if v, _ := strconv.Atoi(r.URL.Query().Get("version")); v > 0 {
				args["version"] = float64(v)
			}
		} else if r.Method == "PUT" {
			action = "update"
		}
	} else if len(parts) == 2 {
		args["process_id"] = parts[0]
		if parts[1] == "runs" && r.Method == "GET" {
			action = "runs"
		} else if r.Method == "POST" {
			switch parts[1] {
			case "activate", "pause", "archive", "start":
				action = parts[1]
			}
		}
	}
	if len(parts) >= 2 && parts[1] == "assignments" {
		args["process_id"] = parts[0]
		if len(parts) == 2 {
			if r.Method == "GET" {
				action = "assignments"
			}
			if r.Method == "POST" {
				action = "assignment_create"
			}
		}
		if len(parts) == 3 {
			args["assignment_id"] = parts[2]
			if r.Method == "GET" {
				action = "assignment_get"
			}
			if r.Method == "PUT" {
				action = "assignment_update"
			}
		}
		if len(parts) == 4 && r.Method == "POST" {
			args["assignment_id"] = parts[2]
			switch parts[3] {
			case "activate", "pause", "archive":
				action = "assignment_" + parts[3]
			case "start":
				action = "start"
			}
		}
	}
	if len(parts) >= 3 && parts[1] == "runs" {
		args["process_id"] = parts[0]
		args["run_id"] = parts[2]
		if len(parts) == 4 && parts[3] == "cancel" && r.Method == "POST" {
			action = "run_cancel"
		}
		if len(parts) == 3 && r.Method == "GET" {
			action = "run_get"
		}
		if len(parts) == 5 && parts[3] == "steps" {
			args["step_id"] = parts[4]
			if r.Method == "GET" {
				action = "step_get"
			}
			if r.Method == "POST" {
				action = "step_update"
			}
		}
	}
	if (path == "trigger-sources" || (len(parts) == 2 && parts[1] == "trigger-sources")) && r.Method == "GET" {
		action = "trigger_sources"
	}
	if len(parts) == 4 && parts[1] == "assignments" && parts[3] == "triggers" {
		args["process_id"] = parts[0]
		args["assignment_id"] = parts[2]
		if r.Method == "GET" {
			action = "triggers"
		}
		if r.Method == "POST" {
			action = "trigger_create"
		}
	}
	if len(parts) >= 3 && parts[1] == "triggers" {
		args["process_id"] = parts[0]
		args["trigger_id"] = parts[2]
		if len(parts) == 3 {
			if r.Method == "GET" {
				action = "trigger_get"
			}
			if r.Method == "PUT" {
				action = "trigger_update"
			}
		}
		if len(parts) == 4 {
			if r.Method == "GET" && parts[3] == "events" {
				action = "trigger_events"
			}
			if r.Method == "POST" {
				switch parts[3] {
				case "activate", "pause", "preview", "test_run", "event_retry":
					action = "trigger_" + parts[3]
				}
			}
		}
	}
	if parts[0] == "tasks" || path == "task-runs" {
		args = map[string]any{}
		action = ""
		if path == "task-runs" && r.Method == "GET" {
			action = "task_runs"
		}
		if path == "tasks" {
			if r.Method == "POST" {
				action = "task_create"
			}
			if r.Method == "GET" {
				action = "tasks"
				for _, k := range []string{"assignee", "state", "origin", "run_id", "process_id", "search"} {
					args[k] = r.URL.Query().Get(k)
				}
				args["overdue"] = r.URL.Query().Get("overdue") == "true"
				for _, k := range []string{"limit", "offset"} {
					v, _ := strconv.Atoi(r.URL.Query().Get(k))
					args[k] = float64(v)
				}
			}
		}
		if len(parts) == 2 && parts[0] == "tasks" {
			args["task_id"] = parts[1]
			if r.Method == "GET" {
				action = "task_get"
			}
			if r.Method == "PUT" {
				action = "task_update"
			}
		}
		if len(parts) == 3 && parts[0] == "tasks" && parts[2] == "cancel" && r.Method == "POST" {
			args["task_id"] = parts[1]
			action = "task_cancel"
		}
	}

	if action == "" {
		http.Error(w, "unsupported route or method", 405)
		return
	}
	if r.Method != "GET" {
		var body map[string]any
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 160*1024))
		err := decoder.Decode(&body)
		if err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON", 400)
			return
		}
		for k, v := range body {
			if k == "run_id" && action == "task_create" {
				args[k] = v
				continue
			}
			if k != "task_id" && k != "process_id" && k != "project_id" && k != "_project_id" && k != "run_id" && k != "step_id" && k != "trigger_id" && (k != "assignment_id" || args["assignment_id"] == nil) {
				args[k] = v
			}
		}
	}
	result, err := a.execute(project, "operator", action, args)
	if err != nil {
		status := 400
		if errors.Is(err, errNotFound) {
			status = 404
		}
		if errors.Is(err, errConflict) {
			status = 409
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	status := 200
	if p, ok := result.(*Process); ok && p.SyncPending {
		status = 202
	}
	if x, ok := result.(*Assignment); ok && x.SyncPending {
		status = 202
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func assignmentSchema() map[string]any {
	d := definitionSchema()["properties"].(map[string]any)
	return object([]string{"name", "owner_agent_id", "execution_mode"}, map[string]any{
		"name": textField("Assignment name, for example Photography Patreon"), "target": textField("Page, client, business or other target; never credentials"), "owner_agent_id": d["owner_agent_id"], "execution_mode": d["execution_mode"], "schedule": d["schedule"], "procedure_version": map[string]any{"type": "integer", "minimum": 1}, "roles": map[string]any{"type": "object", "additionalProperties": executorSchema()}, "follow_latest": map[string]any{"type": "boolean", "description": "Adopt future procedure revisions; otherwise pin procedure_version"}, "parameters": map[string]any{"type": "object", "description": "Values for the procedure's declared parameters; use authorized connection references, not credentials"}})
}

func executorSchema() map[string]any {
	return object([]string{"kind"}, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"agent", "human"}}, "agent_id": map[string]any{"type": "integer", "minimum": 1}})
}
func stepSchema() map[string]any {
	return object([]string{"key", "name", "role", "kind", "instructions", "expected_output"}, map[string]any{"key": textField("Unique step key"), "name": textField("Step name"), "role": textField("Role key bound to an executor by each assignment"), "kind": map[string]any{"type": "string", "enum": []string{"work", "approval"}}, "instructions": textField("Instructions for this step only"), "expected_output": textField("Required output and evidence"), "depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}})
}
