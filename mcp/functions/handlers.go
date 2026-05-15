package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// ─── HTTP handlers ─────────────────────────────────────────────────

func (a *App) handleHTTPFunctionsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPListFunctions(w, r)
	case http.MethodPost:
		a.handleHTTPCreateFunction(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPFunctionItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/functions/")
	parts := strings.SplitN(rest, "/", 2)
	idStr := parts[0]
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}

	if len(parts) == 2 {
		switch parts[1] {
		case "invocations":
			if r.Method != http.MethodGet {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPFunctionInvocations(w, r, id)
			return
		case "invoke":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPInvokeByID(w, r, id)
			return
		case "versions":
			if r.Method != http.MethodGet {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPFunctionVersions(w, r, id)
			return
		case "deploy":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPDeployFunction(w, r, id)
			return
		case "rollback":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			a.handleHTTPRollbackFunction(w, r, id)
			return
		}
		httpErr(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.handleHTTPGetFunction(w, r, id)
	case http.MethodPatch, http.MethodPut:
		a.handleHTTPUpdateFunction(w, r, id)
	case http.MethodDelete:
		a.handleHTTPDeleteFunction(w, r, id)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPListFunctions(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	out, err := dbListFunctions(globalCtx.AppDB(), pid, FunctionFilter{
		Runtime: q.Get("runtime"),
		Status:  q.Get("status"),
		Limit:   atoiDefault(q.Get("limit"), 100, 500),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"functions": out})
}

func (a *App) handleHTTPCreateFunction(w http.ResponseWriter, r *http.Request) {
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
	fn, err := buildAndCreateFunction(globalCtx, pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"function": fn})
}

func (a *App) handleHTTPGetFunction(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fn, err := dbGetFunction(globalCtx.AppDB(), pid, id, "")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if fn == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"function": fn})
}

func (a *App) handleHTTPUpdateFunction(w http.ResponseWriter, r *http.Request, id int64) {
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
	fn, err := updateFunctionMeta(globalCtx, pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"function": fn})
}

// handleHTTPDeployFunction builds a new version and makes it active.
func (a *App) handleHTTPDeployFunction(w http.ResponseWriter, r *http.Request, id int64) {
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
	fn, ver, err := deployFromArgs(globalCtx, pid, id, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"function": fn, "version": ver})
}

// handleHTTPRollbackFunction repoints the active version at an older one.
func (a *App) handleHTTPRollbackFunction(w http.ResponseWriter, r *http.Request, id int64) {
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
	version := intArg(body, "version", 0)
	if version <= 0 {
		httpErr(w, http.StatusBadRequest, "version (positive integer) required")
		return
	}
	ver, err := rollbackFunction(globalCtx, pid, id, version)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fn, _ := dbGetFunction(globalCtx.AppDB(), pid, id, "")
	httpJSON(w, map[string]any{"function": fn, "version": ver})
}

// handleHTTPFunctionVersions lists a function's deploy history.
func (a *App) handleHTTPFunctionVersions(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 100)
	out, err := dbListVersions(globalCtx.AppDB(), pid, id, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"versions": out})
}

func (a *App) handleHTTPDeleteFunction(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbDeleteFunction(globalCtx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"deleted": true, "id": id})
}

func (a *App) handleHTTPFunctionInvocations(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	out, err := dbListInvocations(globalCtx.AppDB(), pid, id, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"invocations": out})
}

func (a *App) handleHTTPInvocationsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	out, err := dbRecentInvocations(globalCtx.AppDB(), pid, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"invocations": out})
}

// handleHTTPInvokeByName powers the auto-routed /fn/<name> endpoint.
// The request body is treated as the event payload; the response is
// the function's stdout (verbatim, content-type-tagged JSON when it
// parses, otherwise text).
func (a *App) handleHTTPInvokeByName(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/fn/")
	if name == "" || strings.Contains(name, "/") {
		httpErr(w, http.StatusBadRequest, "function name required")
		return
	}
	fn, err := dbGetFunction(globalCtx.AppDB(), pid, 0, name)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if fn == nil {
		httpErr(w, http.StatusNotFound, "function not found")
		return
	}
	event := decodeEventBody(r)
	a.runAndWriteResponse(w, r, fn, event, "http")
}

func (a *App) handleHTTPInvokeByID(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fn, err := dbGetFunction(globalCtx.AppDB(), pid, id, "")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if fn == nil {
		httpErr(w, http.StatusNotFound, "function not found")
		return
	}
	event := decodeEventBody(r)
	a.runAndWriteResponse(w, r, fn, event, "http")
}

// runAndWriteResponse is the shared tail for both /fn/<name> and
// /functions/<id>/invoke. Surfaces the function's stdout as the
// HTTP response body when the run succeeds; on error / timeout
// returns 500 with the error message — callers reading from jobs
// see the non-2xx and retry on schedule.
func (a *App) runAndWriteResponse(w http.ResponseWriter, r *http.Request, fn *Function, event any, trigger string) {
	res, err := invokeFunction(globalCtx, r.Context(), fn, event, trigger)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("X-Apteva-Function-Invocation", strconv.FormatInt(res.InvocationID, 10))
	w.Header().Set("X-Apteva-Function-Status", res.Status)
	if res.Status != "ok" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":         res.Error,
			"status":        res.Status,
			"exit_code":     res.ExitCode,
			"invocation_id": res.InvocationID,
			"stderr":        res.Stderr,
		})
		return
	}
	// Tag JSON-shaped responses with application/json so the caller
	// (often jobs.dispatchClient) can parse without sniffing.
	if looksLikeJSON(res.Response) {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write([]byte(res.Response))
}

// decodeEventBody pulls the event payload from the request. JSON
// bodies decode into a map/slice; non-JSON bodies surface as
// {"raw":"<bytes>"} so the function can still inspect them. Empty
// body becomes nil — JSON.parse of "null" is valid in every
// runtime we support.
func decodeEventBody(r *http.Request) any {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	// Cap the read so a malicious caller can't OOM the sidecar with
	// a chunked stream. 1MB is generous for "an event payload."
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
	if err := json.Unmarshal(body, &parsed); err == nil {
		return parsed
	}
	return map[string]any{"raw": string(body)}
}

// readAll fills buf from r until full or EOF. Returns bytes read.
// Inlined so handlers.go doesn't need io.ReadFull's "exactly N"
// semantics (we want best-effort up-to-N).
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

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	c := s[0]
	return c == '{' || c == '[' || c == '"' || c == 't' || c == 'f' || c == 'n' || (c >= '0' && c <= '9') || c == '-'
}

// ─── MCP tool handlers ─────────────────────────────────────────────

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fn, err := buildAndCreateFunction(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("function.created", map[string]any{
			"id":      fn.ID,
			"name":    fn.Name,
			"runtime": fn.Runtime,
		})
	}
	return map[string]any{"function": fn}, nil
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	fn, err := updateFunctionMeta(ctx, pid, id, args)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("function.updated", map[string]any{"id": fn.ID, "name": fn.Name})
	}
	return map[string]any{"function": fn}, nil
}

// toolDeploy builds a new version of an existing function and makes
// it active.
func (a *App) toolDeploy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	fn, ver, err := deployFromArgs(ctx, pid, id, args)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("function.deployed", map[string]any{"id": id, "name": fn.Name, "version": ver.Version})
	}
	return map[string]any{"function": fn, "version": ver}, nil
}

// toolRollback repoints a function's active version at an older one.
func (a *App) toolRollback(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	version := intArg(args, "version", 0)
	if version <= 0 {
		return nil, errors.New("version (positive integer) required")
	}
	ver, err := rollbackFunction(ctx, pid, id, version)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("function.deployed", map[string]any{"id": id, "version": ver.Version, "rollback": true})
	}
	fn, _ := dbGetFunction(dbFor(ctx), pid, id, "")
	return map[string]any{"function": fn, "version": ver}, nil
}

// toolVersions lists a function's deploy history.
func (a *App) toolVersions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListVersions(dbFor(ctx), pid, id, intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"versions": out}, nil
}

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if err := dbDeleteFunction(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("function.deleted", map[string]any{"id": id})
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbListFunctions(ctx.AppDB(), pid, FunctionFilter{
		Runtime: strArg(args, "runtime"),
		Status:  strArg(args, "status"),
		Limit:   intArg(args, "limit", 100),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"functions": out, "count": len(out)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fn, err := dbGetFunction(ctx.AppDB(), pid, int64Arg(args, "id"), strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return map[string]any{"function": nil, "found": false}, nil
	}
	return map[string]any{"function": fn, "found": true}, nil
}

func (a *App) toolInvoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fn, err := dbGetFunction(ctx.AppDB(), pid, int64Arg(args, "id"), strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, errors.New("function not found")
	}
	event := args["event"]
	res, err := invokeFunction(ctx, context.Background(), fn, event, "manual")
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"invocation_id": res.InvocationID,
		"status":        res.Status,
		"duration_ms":   res.DurationMS,
		"exit_code":     res.ExitCode,
		"response":      res.Response,
	}
	if res.Stderr != "" {
		out["stderr"] = res.Stderr
	}
	if res.Error != "" {
		out["error"] = res.Error
	}
	return out, nil
}

func (a *App) toolInvocations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := resolveFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListInvocations(ctx.AppDB(), pid, id, intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"invocations": out}, nil
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invocation_id")
	if id == 0 {
		return nil, errors.New("invocation_id required")
	}
	inv, err := dbGetInvocation(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, errors.New("invocation not found")
	}
	return map[string]any{
		"invocation_id": inv.ID,
		"function_id":   inv.FunctionID,
		"stdout":        inv.ResponseBody,
		"stderr":        inv.Stderr,
		"error":         inv.Error,
		"status":        inv.Status,
		"exit_code":     inv.ExitCode,
		"started_at":    inv.StartedAt,
		"finished_at":   inv.FinishedAt,
	}, nil
}

// ─── Shared create / update / deploy plumbing ──────────────────────
//
// Both HTTP POST /functions and the MCP functions_create tool funnel
// through buildAndCreateFunction; deploy + rollback live in deploy.go.

// buildAndCreateFunction inserts the function definition row, then
// deploys v1 (which builds it and makes it active). A failed first
// build rolls the bare row back so no unrunnable function lingers.
func buildAndCreateFunction(ctx *sdk.AppCtx, pid string, args map[string]any) (*Function, error) {
	fn := &Function{
		ProjectID:   pid,
		Name:        strArg(args, "name"),
		Runtime:     strArg(args, "runtime"),
		SourceKind:  strArg(args, "source_kind"),
		Source:      strArg(args, "source"),
		RepoPath:    strArg(args, "repo_path"),
		TimeoutMS:   intArg(args, "timeout_ms", defaultTimeout),
		MaxMemoryMB: intArg(args, "max_memory_mb", defaultMemoryMB),
	}
	if rid := int64Arg(args, "repo_id"); rid != 0 {
		fn.RepoID = &rid
	}
	if envMap, ok := args["env"].(map[string]any); ok {
		fn.Env = map[string]string{}
		for k, v := range envMap {
			if s, ok := v.(string); ok {
				fn.Env[k] = s
			}
		}
	}
	if fn.SourceKind == "" {
		// Imply source_kind from the fields the caller supplied.
		if fn.Source != "" {
			fn.SourceKind = "inline"
		} else if fn.RepoID != nil {
			fn.SourceKind = "repo"
		}
	}
	// Stamp a hash for the bare row; deployVersion overwrites the
	// denormalised source columns once v1 is resolved + built.
	if fn.SourceKind == "inline" {
		fn.SourceHash = hashSource([]byte(fn.Source))
	} else {
		fn.SourceHash = "pending"
	}

	created, err := dbCreateFunction(dbFor(ctx), pid, fn)
	if err != nil {
		return nil, err
	}

	if _, err := deployVersion(ctx, created, created.SourceKind, created.Source,
		created.RepoID, created.RepoPath, strArg(args, "package_json")); err != nil {
		_ = dbDeleteFunction(dbFor(ctx), pid, created.ID)
		return nil, err
	}
	return dbGetFunction(dbFor(ctx), pid, created.ID, "")
}

// updateFunctionMeta patches metadata only — env, timeout_ms,
// max_memory_mb, status. Source / runtime changes are immutable per
// version: they go through functions_deploy, which builds a fresh
// version, not functions_update.
func updateFunctionMeta(ctx *sdk.AppCtx, pid string, id int64, patch map[string]any) (*Function, error) {
	for _, k := range []string{"source", "source_kind", "repo_id", "repo_path", "package_json", "runtime"} {
		if _, has := patch[k]; has {
			return nil, fmt.Errorf("%q can't be changed with functions_update — use functions_deploy", k)
		}
	}
	cur, err := dbGetFunction(dbFor(ctx), pid, id, "")
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errors.New("function not found")
	}
	return dbUpdateFunction(dbFor(ctx), pid, id, patch, "")
}

// resolveFunctionID accepts either id or name and returns the row's
// id. Centralised so every tool that takes "id or name" agrees on
// the resolution rules.
func resolveFunctionID(ctx *sdk.AppCtx, pid string, args map[string]any) (int64, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return id, nil
	}
	name := strArg(args, "name")
	if name == "" {
		return 0, errors.New("id or name required")
	}
	fn, err := dbGetFunction(dbFor(ctx), pid, 0, name)
	if err != nil {
		return 0, err
	}
	if fn == nil {
		return 0, errors.New("function not found")
	}
	return fn.ID, nil
}

// dbFor returns the AppCtx-bound *sql.DB. Tests build their own
// AppCtx via testkit.NewAppCtx and call create/update through these
// shared helpers; production handlers pass the package-level
// globalCtx in. Both satisfy "give me ctx, I'll give you the DB".
func dbFor(ctx *sdk.AppCtx) *sql.DB {
	if ctx != nil {
		return ctx.AppDB()
	}
	return nil
}

// ─── Examples endpoint ─────────────────────────────────────────────
//
// GET /examples?runtime=node|go → { examples: [{ name, runtime,
// source, description }] }. The handler files live in examples/ on
// disk and are embedded into the binary at build time; the panel's
// "Load" picker calls this to populate itself.

func (a *App) handleHTTPExamples(w http.ResponseWriter, r *http.Request) {
	runtimeFilter := r.URL.Query().Get("runtime")
	entries, err := examplesFS.ReadDir("examples")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type exDTO struct {
		Name        string `json:"name"`
		Runtime     string `json:"runtime"`
		Source      string `json:"source"`
		Description string `json:"description,omitempty"`
	}
	out := []exDTO{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, rt := parseExampleFilename(e.Name())
		if rt == "" {
			continue
		}
		if runtimeFilter != "" && rt != runtimeFilter {
			continue
		}
		src, err := examplesFS.ReadFile("examples/" + e.Name())
		if err != nil {
			continue
		}
		out = append(out, exDTO{
			Name:        name,
			Runtime:     rt,
			Source:      string(src),
			Description: firstDescLine(src),
		})
	}
	httpJSON(w, map[string]any{"examples": out})
}

// parseExampleFilename maps an example filename to (name, runtime).
// Returns ("", "") for files that aren't a recognised example.
func parseExampleFilename(name string) (string, string) {
	switch {
	case strings.HasSuffix(name, ".mjs"):
		return strings.TrimSuffix(name, ".mjs"), "node"
	case strings.HasSuffix(name, ".go.txt"):
		return strings.TrimSuffix(name, ".go.txt"), "go"
	}
	return "", ""
}

// firstDescLine pulls a short description from the first comment
// line of an example file, with the leading "// " and any
// "<name> —" prefix stripped. Returns "" if the file doesn't start
// with a comment.
func firstDescLine(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			return ""
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "//"))
		if line == "" {
			continue
		}
		if i := strings.Index(line, " — "); i > 0 {
			line = line[i+len(" — "):]
		}
		return line
	}
	return ""
}
