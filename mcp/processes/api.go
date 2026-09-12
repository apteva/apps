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
		"execution_mode": map[string]any{"type": "string", "enum": []string{"agent", "tasks"}, "description": "Default agent; tasks requires the optional Tasks integration"}, "name": textField("Procedure name"), "description": textField("Purpose"), "instructions": textField("Ordered steps or checklist in plain language"), "required_inputs": textField("Inputs or sources the owner must obtain"), "default_inputs": textField("Standing execution context"), "completion_criteria": textField("Required outcomes and evidence"), "approval_requirements": textField("Explicit approval checkpoints; does not enforce a software gate"), "owner_agent_id": map[string]any{"type": "integer", "minimum": 1}, "schedule": object([]string{"kind"}, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"interval", "cron"}}, "every": textField("Duration, e.g. 24h; minimum 1m"), "cron": textField("Five-field cron expression"), "timezone": textField("IANA timezone; default UTC")}),
	})
}
func (a *App) MCPTools() []sdk.Tool {
	descriptions := map[string]string{"list": "Find project procedures by search, status, or owner.", "get": "Read a procedure and immutable versions. Specify version to retrieve a historical definition.", "create": "Create a draft company procedure only when authorized to define company policy.", "update": "Replace the definition with a new immutable version. Requires a draft or fully paused process and expected_version.", "activate": "Activate a procedure and its schedule. Check sync_pending before claiming success.", "pause": "Stop future scheduled runs. Existing runs continue. Check sync_pending.", "archive": "Retire a process and stop future schedules. Existing runs continue.", "start": "Start one active procedure on its owner agent. Supply a stable idempotency_key and reuse it on retries. Track execution in the selected backend.", "runs": "Read direct runs and live Tasks history when used."}
	descriptions["run_get"] = "Read the immutable procedure and direct run before acting."
	descriptions["run_update"] = "Owner only: record direct run progress, blockers, or outcome. Completion requires evidence in result."
	out := []sdk.Tool{}
	for _, name := range []string{"list", "get", "create", "update", "activate", "pause", "archive", "start", "runs", "run_get", "run_update"} {
		name := name
		props := map[string]any{}
		required := []string{}
		if name != "list" && name != "create" {
			props["process_id"] = textField("Process ID")
			required = append(required, "process_id")
		}
		switch name {
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
	return out
}
func (a *App) execute(project, actor, action string, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
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
			if owner := number(args, "owner_agent_id"); owner > 0 && p.OwnerAgentID != int64(owner) {
				continue
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
		return a.start(project, id, str(args, "idempotency_key"), str(args, "inputs"))
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
			if k != "process_id" && k != "project_id" && k != "_project_id" {
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
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
