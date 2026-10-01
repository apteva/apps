package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml migrations/*.sql
var embedded embed.FS

type App struct {
	db        *sql.DB
	ctx       *sdk.AppCtx
	now       func() time.Time
	providers func(*sdk.AppCtx, string) ([]Provider, error)
	call      func(context.Context, *sdk.AppCtx, Provider, string, map[string]any) (json.RawMessage, error)
}

type appError struct {
	Code    string
	Status  int
	Message string
}

func (e *appError) Error() string                        { return e.Message }
func fail(code string, status int, message string) error { return &appError{code, status, message} }
func (a *App) clock() int64 {
	if a.now != nil {
		return a.now().Unix()
	}
	return time.Now().Unix()
}
func (a *App) Manifest() sdk.Manifest {
	raw, _ := embedded.ReadFile("apteva.yaml")
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	a.db = ctx.AppDB()
	a.ctx = ctx
	if a.db == nil {
		return errors.New("sportsbook requires SQLite")
	}
	_, err := a.db.Exec("PRAGMA foreign_keys=ON")
	return err
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker             { return nil } // v0.1 refreshes explicitly to protect provider quotas.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/rpc", Handler: a.handleRPC}, {Pattern: "/", Handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "./ui/index.html", http.StatusSeeOther)
	}}}
}
func permission(tool string) string {
	switch tool {
	case "workspace_get", "integrations_list", "odds_history":
		return "read"
	case "provider_route_set", "demo_load", "bankroll_create":
		return "configure"
	case "sports_sync", "odds_sync":
		return "sync"
	case "prediction_run", "prediction_explain", "bet_propose":
		return "propose"
	case "proposal_accept":
		return "paper_execute"
	case "bet_settle":
		return "paper_settle"
	case "bet_submit":
		return "live_execute"
	}
	return ""
}
func authorize(callCtx context.Context, tool, project string) error {
	p := permission(tool)
	if p == "" {
		return fail("unknown_tool", 404, "Unknown operation")
	}
	caller := sdk.CallerFrom(callCtx)
	// Do not inherit the SDK's legacy nil-caller allow rule for mutations.
	if caller == nil {
		return fail("caller_required", 403, "Authenticated caller required")
	}
	if caller.ProjectID != "" && caller.ProjectID != project {
		return fail("project_mismatch", 403, "Project mismatch")
	}
	if !caller.Allows("sportsbook."+p, "") {
		return fail("permission_denied", 403, "Permission denied")
	}
	return nil
}
func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, spec := range a.Manifest().Provides.MCPTools {
		name := spec.Name
		out = append(out, sdk.Tool{Name: name, Description: spec.Description, InputSchema: toolSchema(name), HandlerCtx: func(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			project := ctx.CurrentProject()
			if project == "" {
				return nil, fail("project_required", 403, "Project-scoped installation required")
			}
			if err := authorize(c, name, project); err != nil {
				return nil, err
			}
			if v := textArg(args, "project_id"); v != "" && v != project {
				return nil, fail("project_mismatch", 403, "Project mismatch")
			}
			caller := sdk.CallerFrom(c)
			actor := "agent:" + strconv.FormatInt(caller.AgentID, 10)
			if caller.AppInstallID > 0 {
				actor = "app:" + strconv.FormatInt(caller.AppInstallID, 10)
			}
			return a.perform(c, ctx, project, actor, name, args)
		}})
	}
	return out
}
func (a *App) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		writeError(w, fail("method", 405, "POST required"))
		return
	}
	// Agent calls use MCP so the framework supplies their grants. This endpoint
	// serves authenticated dashboard users through the SDK's token-auth proxy.
	if r.Header.Get("X-Apteva-Caller-Agent") != "" || r.Header.Get("X-Apteva-Caller-Instance") != "" || r.Header.Get(sdk.HeaderBoundCallerInstallID) != "" {
		writeError(w, fail("use_mcp", 403, "Agent and app calls must use MCP"))
		return
	}
	project := ""
	if a.ctx != nil {
		project = a.ctx.CurrentProject()
	}
	if project == "" {
		writeError(w, fail("project_required", 403, "Project-scoped installation required"))
		return
	}
	if p := r.URL.Query().Get("project_id"); p != "" && p != project {
		writeError(w, fail("project_mismatch", 403, "Project mismatch"))
		return
	}
	if p := r.Header.Get("X-Apteva-Project-ID"); p != "" && p != project {
		writeError(w, fail("project_mismatch", 403, "Project mismatch"))
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, fail("content_type", 415, "Content-Type must be application/json"))
		return
	}
	var req struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, fail("invalid_json", 400, "Invalid JSON request"))
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeError(w, fail("invalid_json", 400, "Expected one JSON object"))
		return
	}
	if permission(req.Tool) == "" {
		writeError(w, fail("unknown_tool", 404, "Unknown operation"))
		return
	}
	if v := textArg(req.Args, "project_id"); v != "" && v != project {
		writeError(w, fail("project_mismatch", 403, "Project mismatch"))
		return
	}
	ctx := a.ctx.WithUserSession(r)
	actor := "dashboard"
	if r.Header.Get("X-Apteva-Trusted-Principal") != "" || r.Header.Get("X-Apteva-Trusted-Principal-Signature") != "" {
		principal, err := principalFromRequest(r)
		if err != nil || principal == nil {
			writeError(w, fail("invalid_principal", 403, "Invalid user identity"))
			return
		}
		if principal.ProjectID != "" && principal.ProjectID != project {
			writeError(w, fail("project_mismatch", 403, "Project mismatch"))
			return
		}
		actor = "user:" + strconv.FormatInt(principal.UserID, 10)
	}
	result, err := a.perform(r.Context(), ctx, project, actor, req.Tool, req.Args)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	var e *appError
	if errors.As(err, &e) {
		writeJSON(w, e.Status, map[string]any{"error": e.Message, "code": e.Code})
		return
	}
	writeJSON(w, 500, map[string]any{"error": "Operation failed", "code": "internal_error"})
}
func main() { sdk.Run(&App{}) }
