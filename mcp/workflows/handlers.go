package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── HTTP utilities ────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

func dbFor(ctx *sdk.AppCtx) *sql.DB {
	if ctx != nil {
		return ctx.AppDB()
	}
	return nil
}

// ─── HTTP: workflows collection / item ─────────────────────────────

func (a *App) handleHTTPWorkflowsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPListWorkflows(w, r)
	case http.MethodPost:
		a.handleHTTPCreateWorkflow(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPWorkflowItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/workflows/")
	parts := strings.SplitN(rest, "/", 2)
	idStr := parts[0]
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "runs":
			if r.Method != http.MethodGet {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPWorkflowRuns(w, r, id)
			return
		case "run":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPRunByID(w, r, id)
			return
		}
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPGetWorkflow(w, r, id)
	case http.MethodPatch, http.MethodPut:
		a.handleHTTPUpdateWorkflow(w, r, id)
	case http.MethodDelete:
		a.handleHTTPDeleteWorkflow(w, r, id)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPListWorkflows(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	out, err := dbListWorkflows(globalCtx.AppDB(), pid, WorkflowFilter{
		Status:      q.Get("status"),
		TriggerKind: q.Get("trigger_kind"),
		Limit:       atoiDefault(q.Get("limit"), 100, 500),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"workflows": out})
}

func (a *App) handleHTTPCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	wf, err := buildAndCreateWorkflow(globalCtx, pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"workflow": wf})
}

func (a *App) handleHTTPGetWorkflow(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wf, err := dbGetWorkflow(globalCtx.AppDB(), pid, id, "")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if wf == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"workflow": wf})
}

func (a *App) handleHTTPUpdateWorkflow(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	wf, err := updateAndRehashWorkflow(globalCtx, pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"workflow": wf})
}

func (a *App) handleHTTPDeleteWorkflow(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbDeleteWorkflow(globalCtx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"deleted": true, "id": id})
}

func (a *App) handleHTTPWorkflowRuns(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	out, err := dbListRuns(globalCtx.AppDB(), pid, id, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"runs": out})
}

// handleHTTPRunByName is the auto-routed trigger endpoint.
// /api/apps/workflows/wf/<name> hits this handler; the request
// body becomes the workflow's input. Same shape as
// functions /fn/<name>.
func (a *App) handleHTTPRunByName(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/wf/")
	if name == "" || strings.Contains(name, "/") {
		httpErr(w, http.StatusBadRequest, "workflow name required")
		return
	}
	wf, err := dbGetWorkflow(globalCtx.AppDB(), pid, 0, name)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if wf == nil {
		httpErr(w, http.StatusNotFound, "workflow not found")
		return
	}
	input := decodeBody(r)
	a.runAndWrite(w, r, wf, input, "http")
}

func (a *App) handleHTTPRunByID(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wf, err := dbGetWorkflow(globalCtx.AppDB(), pid, id, "")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if wf == nil {
		httpErr(w, http.StatusNotFound, "workflow not found")
		return
	}
	input := decodeBody(r)
	a.runAndWrite(w, r, wf, input, "http")
}

func (a *App) runAndWrite(w http.ResponseWriter, r *http.Request, wf *Workflow, input any, trigger string) {
	pid, _ := resolveProjectFromRequest(r)
	run, err := RunWorkflow(r.Context(), globalCtx, pid, wf, input, runOptions{triggerKind: trigger})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("X-Apteva-Workflow-Run", strconv.FormatInt(run.ID, 10))
	w.Header().Set("X-Apteva-Workflow-Status", run.Status)
	if run.Status != "completed" {
		w.WriteHeader(http.StatusInternalServerError)
	}
	httpJSON(w, map[string]any{"run": run})
}

// ─── HTTP: runs collection / item ──────────────────────────────────

func (a *App) handleHTTPRunsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	httpErr(w, http.StatusBadRequest, "use /workflows/<id>/runs to list runs")
}

func (a *App) handleHTTPRunItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/runs/")
	id, _ := strconv.ParseInt(rest, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "run_id required")
		return
	}
	run, err := dbGetRun(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	run.Steps, _ = dbListStepExecutions(globalCtx.AppDB(), id)
	httpJSON(w, map[string]any{"run": run})
}

// decodeBody reads the request body as JSON; falls back to wrapping
// non-JSON in {"raw":"<text>"} so workflows can still see it.
// Same shape as functions/handlers.go.
func decodeBody(r *http.Request) any {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	const maxBody = 1 << 20
	buf := make([]byte, maxBody+1)
	n, _ := readAll(r.Body, buf)
	if n == 0 {
		return nil
	}
	if n > maxBody {
		n = maxBody
	}
	body := buf[:n]
	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		return parsed
	}
	return map[string]any{"raw": string(body)}
}

func readAll(r interface {
	Read(p []byte) (int, error)
}, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
	}
	return total, nil
}

// ─── MCP tool handlers ─────────────────────────────────────────────

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wf, err := buildAndCreateWorkflow(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("workflow.created", map[string]any{"id": wf.ID, "name": wf.Name})
	}
	return map[string]any{"workflow": wf}, nil
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveWorkflowID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	wf, err := updateAndRehashWorkflow(ctx, pid, id, args)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("workflow.updated", map[string]any{"id": wf.ID, "name": wf.Name})
	}
	return map[string]any{"workflow": wf}, nil
}

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveWorkflowID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if err := dbDeleteWorkflow(dbFor(ctx), pid, id); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("workflow.deleted", map[string]any{"id": id})
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbListWorkflows(dbFor(ctx), pid, WorkflowFilter{
		Status:      strArg(args, "status"),
		TriggerKind: strArg(args, "trigger_kind"),
		Limit:       intArg(args, "limit", 100),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"workflows": out, "count": len(out)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wf, err := dbGetWorkflow(dbFor(ctx), pid, int64Arg(args, "id"), strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return map[string]any{"workflow": nil, "found": false}, nil
	}
	return map[string]any{"workflow": wf, "found": true}, nil
}

func (a *App) toolRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wf, err := dbGetWorkflow(dbFor(ctx), pid, int64Arg(args, "id"), strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, errors.New("workflow not found")
	}
	run, err := RunWorkflow(context.Background(), ctx, pid, wf, args["input"], runOptions{triggerKind: "manual"})
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": run}, nil
}

func (a *App) toolRuns(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveWorkflowID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListRuns(dbFor(ctx), pid, id, intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": out}, nil
}

func (a *App) toolRunStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "run_id")
	if id == 0 {
		return nil, errors.New("run_id required")
	}
	run, err := dbGetRun(dbFor(ctx), pid, id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("run not found")
	}
	run.Steps, _ = dbListStepExecutions(dbFor(ctx), id)
	return map[string]any{"run": run}, nil
}

func (a *App) toolReplay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "run_id")
	if id == 0 {
		return nil, errors.New("run_id required")
	}
	prev, err := dbGetRun(dbFor(ctx), pid, id)
	if err != nil {
		return nil, err
	}
	if prev == nil {
		return nil, errors.New("run not found")
	}
	wf, err := dbGetWorkflow(dbFor(ctx), pid, prev.WorkflowID, "")
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, errors.New("workflow no longer exists")
	}

	var input any
	if prev.InputJSON != "" {
		_ = json.Unmarshal([]byte(prev.InputJSON), &input)
	}
	run, err := RunWorkflow(context.Background(), ctx, pid, wf, input, runOptions{
		triggerKind: "manual",
		fromStep:    strArg(args, "from_step"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": run}, nil
}

func (a *App) toolCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "run_id")
	if id == 0 {
		return nil, errors.New("run_id required")
	}
	// v0.1 is sync — by the time the agent sees a "running" run it
	// either already completed (status row reflects that) or it's
	// pinned to the calling thread. So cancel = mark as cancelled
	// in the DB, with the understanding that an actively-running
	// step will still complete its current call. Documented limit.
	if err := dbUpdateRunState(dbFor(ctx), pid, id, "cancelled", "", "cancelled by user", true); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("workflow.run.cancelled", map[string]any{"run_id": id})
	}
	return map[string]any{"cancelled": true, "run_id": id}, nil
}

// ─── Shared create / update plumbing ───────────────────────────────

func buildAndCreateWorkflow(ctx *sdk.AppCtx, pid string, args map[string]any) (*Workflow, error) {
	wf := &Workflow{
		ProjectID:   pid,
		Name:        strArg(args, "name"),
		SourceKind:  strArg(args, "source_kind"),
		Source:      strArg(args, "source"),
		RepoPath:    strArg(args, "repo_path"),
		TriggerKind: strArg(args, "trigger_kind"),
		TriggerJSON: strArg(args, "trigger_json"),
	}
	if rid := int64Arg(args, "repo_id"); rid != 0 {
		wf.RepoID = &rid
	}
	if wf.SourceKind == "" {
		if wf.Source != "" {
			wf.SourceKind = "inline"
		} else if wf.RepoID != nil {
			wf.SourceKind = "repo"
		}
	}

	bytes, err := preCreateResolveSource(ctx, wf)
	if err != nil {
		return nil, err
	}
	wf.SourceHash = hashSource(bytes)

	// Validate the source parses + structural checks pass before
	// committing the row. Catches typos / bad ids at create time
	// instead of at first run.
	if def, err := ParseDefinition(bytes); err != nil {
		return nil, err
	} else if wf.TriggerKind == "" {
		wf.TriggerKind = def.Trigger.Kind
	}

	return dbCreateWorkflow(dbFor(ctx), pid, wf)
}

func updateAndRehashWorkflow(ctx *sdk.AppCtx, pid string, id int64, patch map[string]any) (*Workflow, error) {
	cur, err := dbGetWorkflow(dbFor(ctx), pid, id, "")
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errors.New("workflow not found")
	}

	rehash := false
	for _, k := range []string{"source", "source_kind", "repo_id", "repo_path"} {
		if _, has := patch[k]; has {
			rehash = true
			break
		}
	}

	newHash := ""
	if rehash {
		merged := *cur
		if v, ok := patch["source_kind"].(string); ok && v != "" {
			merged.SourceKind = v
		}
		if _, has := patch["source"]; has {
			merged.Source = strArg(patch, "source")
		}
		if rid := int64Arg(patch, "repo_id"); rid != 0 {
			merged.RepoID = &rid
		}
		if v, ok := patch["repo_path"].(string); ok {
			merged.RepoPath = v
		}
		bytes, err := preCreateResolveSource(ctx, &merged)
		if err != nil {
			return nil, err
		}
		// Re-parse the *new* source to fail fast on broken updates.
		if _, err := ParseDefinition(bytes); err != nil {
			return nil, err
		}
		newHash = hashSource(bytes)
	}

	return dbUpdateWorkflow(dbFor(ctx), pid, id, patch, newHash)
}

func preCreateResolveSource(ctx *sdk.AppCtx, wf *Workflow) ([]byte, error) {
	if wf.SourceKind == "inline" {
		return []byte(wf.Source), nil
	}
	if wf.RepoID == nil || wf.RepoPath == "" {
		return nil, errors.New("repo_id and repo_path required for repo source")
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return []byte("repo://" + wf.RepoPath), nil
	}
	var resp struct {
		Content string `json:"content"`
	}
	if err := ctx.PlatformAPI().CallAppResult("code", "code_read_file", map[string]any{
		"repo_id": *wf.RepoID,
		"path":    wf.RepoPath,
	}, &resp); err != nil {
		return nil, err
	}
	return []byte(resp.Content), nil
}

func resolveWorkflowID(ctx *sdk.AppCtx, pid string, args map[string]any) (int64, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return id, nil
	}
	name := strArg(args, "name")
	if name == "" {
		return 0, errors.New("id or name required")
	}
	wf, err := dbGetWorkflow(dbFor(ctx), pid, 0, name)
	if err != nil {
		return 0, err
	}
	if wf == nil {
		return 0, errors.New("workflow not found")
	}
	return wf.ID, nil
}
