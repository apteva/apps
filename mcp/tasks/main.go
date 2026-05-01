// Apteva Tasks app — mission board for an Apteva instance.
//
// This is the canonical example of an Apteva App: it ships an MCP
// server (tools the agent calls), a tiny REST surface for the
// dashboard, a SQLite store that lives in the app's volume, and a
// UI panel embedded into Apteva's instance detail page.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Embedded manifest — apps may load from disk or embed at compile time.
// We embed so the running binary is self-describing.
const manifestYAML = `schema: apteva-app/v1
name: tasks
display_name: Tasks
version: 1.0.0
description: Mission board for an Apteva instance.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.instances.read
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: tasks_create
      description: Create a new task on this instance's mission board.
    - name: tasks_update
      description: Update a task's status, title, or notes.
    - name: tasks_complete
      description: Mark a task as done.
    - name: tasks_list
      description: List the open or recent tasks for this instance.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/tasks
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/tasks.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("tasks app requires a db block")
	}
	ctx.Logger().Info("tasks app mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

// HTTPRoutes — REST surface for the panel. Reverse-proxied at
// /api/apps/tasks/* by apteva-server.
//
// Go's ServeMux refuses duplicate-pattern registrations, so each
// pattern dispatches to the right handler by method internally.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/instances/", Handler: a.handleInstancesItem}, // GET (list) + POST (create)
		{Pattern: "/tasks/", Handler: a.handleTasksItem},         // PUT (update) + DELETE
	}
}

// handleInstancesItem dispatches /instances/<id> by method.
func (a *App) handleInstancesItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleList(w, r)
	case http.MethodPost:
		a.handleCreate(w, r)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleTasksItem dispatches /tasks/<id> by method.
func (a *App) handleTasksItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		a.handleUpdate(w, r)
	case http.MethodDelete:
		a.handleDelete(w, r)
	default:
		http.Error(w, "PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

// MCPTools — the agent-facing surface. Same handlers as HTTP, fronted
// through the app SDK's MCP server at /mcp.
func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "tasks_create",
			Description: "Create a task on this instance's mission board. Args: instance_id, title, notes (optional).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instance_id": map[string]any{"type": "integer"},
					"title":       map[string]any{"type": "string"},
					"notes":       map[string]any{"type": "string"},
				},
				"required": []string{"instance_id", "title"},
			},
			Handler: a.toolCreate,
		},
		{
			Name:        "tasks_list",
			Description: "List tasks for this instance. Args: instance_id, status (open | done | all, default open).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instance_id": map[string]any{"type": "integer"},
					"status":      map[string]any{"type": "string"},
				},
				"required": []string{"instance_id"},
			},
			Handler: a.toolList,
		},
		{
			Name:        "tasks_update",
			Description: "Update a task. Args: task_id, title (optional), notes (optional), status (optional).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "integer"},
					"title":   map[string]any{"type": "string"},
					"notes":   map[string]any{"type": "string"},
					"status":  map[string]any{"type": "string"},
				},
				"required": []string{"task_id"},
			},
			Handler: a.toolUpdate,
		},
		{
			Name:        "tasks_complete",
			Description: "Mark a task as done. Args: task_id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "integer"},
				},
				"required": []string{"task_id"},
			},
			Handler: a.toolComplete,
		},
	}
}

func (a *App) Channels() []sdk.ChannelFactory   { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// --- DB helpers --------------------------------------------------------------

type Task struct {
	ID         int64  `json:"id"`
	InstanceID int64  `json:"instance_id"`
	Title      string `json:"title"`
	Notes      string `json:"notes"`
	Status     string `json:"status"` // open | done
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func dbInsert(db *sql.DB, instanceID int64, title, notes string) (*Task, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(
		`INSERT INTO tasks (instance_id, title, notes, status, created_at, updated_at) VALUES (?,?,?, 'open', ?, ?)`,
		instanceID, title, notes, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Task{ID: id, InstanceID: instanceID, Title: title, Notes: notes, Status: "open", CreatedAt: now, UpdatedAt: now}, nil
}

func dbList(db *sql.DB, instanceID int64, status string) ([]Task, error) {
	q := `SELECT id, instance_id, title, COALESCE(notes,''), status, created_at, updated_at FROM tasks WHERE instance_id = ?`
	args := []any{instanceID}
	if status != "" && status != "all" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT 200`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.InstanceID, &t.Title, &t.Notes, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func dbUpdate(db *sql.DB, taskID int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	cols := []string{}
	args := []any{}
	for _, k := range []string{"title", "notes", "status"} {
		if v, ok := fields[k]; ok {
			cols = append(cols, k+" = ?")
			args = append(args, fmt.Sprint(v))
		}
	}
	if len(cols) == 0 {
		return nil
	}
	cols = append(cols, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339), taskID)
	_, err := db.Exec(`UPDATE tasks SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func dbDelete(db *sql.DB, taskID int64) error {
	_, err := db.Exec(`DELETE FROM tasks WHERE id = ?`, taskID)
	return err
}

// --- HTTP handlers ----------------------------------------------------------

func (a *App) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/instances/")
	tasks, err := dbList(ctx.AppDB(), id, r.URL.Query().Get("status"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, tasks)
}

func (a *App) handleCreate(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/instances/")
	var body struct{ Title, Notes string }
	json.NewDecoder(r.Body).Decode(&body)
	if body.Title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	t, err := dbInsert(ctx.AppDB(), id, body.Title, body.Notes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, t)
}

func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/tasks/")
	var fields map[string]any
	json.NewDecoder(r.Body).Decode(&fields)
	if err := dbUpdate(ctx.AppDB(), id, fields); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/tasks/")
	if err := dbDelete(ctx.AppDB(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- MCP tool handlers ------------------------------------------------------

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["instance_id"])
	title, _ := args["title"].(string)
	notes, _ := args["notes"].(string)
	if id == 0 || title == "" {
		return nil, errors.New("instance_id and title required")
	}
	return dbInsert(ctx.AppDB(), id, title, notes)
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["instance_id"])
	status, _ := args["status"].(string)
	if status == "" {
		status = "open"
	}
	return dbList(ctx.AppDB(), id, status)
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["task_id"])
	if id == 0 {
		return nil, errors.New("task_id required")
	}
	if err := dbUpdate(ctx.AppDB(), id, args); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "task_id": id}, nil
}

func (a *App) toolComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["task_id"])
	if id == 0 {
		return nil, errors.New("task_id required")
	}
	if err := dbUpdate(ctx.AppDB(), id, map[string]any{"status": "done"}); err != nil {
		return nil, err
	}
	return map[string]any{"status": "done", "task_id": id}, nil
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func pathSuffixInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

// mustCtx — placeholder that returns a no-op context. The framework
// will replace this when we wire per-request context propagation; for
// now the in-process *AppCtx held by the App suffices for HTTP
// handlers because the SDK's Run loop mounts them inside the same
// process. Stage-2 cleanup.
var globalCtx *sdk.AppCtx

func mustCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

func main() {
	app := &App{}
	// Hook into OnMount to capture the ctx for HTTP handlers.
	// The cleaner path is to extend the SDK so handlers receive ctx
	// directly; doing that in v2 of app-sdk.
	wrapped := wrapApp{app: app}
	sdk.Run(&wrapped)
}

// wrapApp is a tiny shim so the package-global *AppCtx gets populated
// before HTTP routes start serving. Keeps the public App API clean
// while we evolve the SDK to thread ctx through HandlerFunc.
type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest        { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route       { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool          { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker          { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler { return w.app.EventHandlers() }
