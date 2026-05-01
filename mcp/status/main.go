// Apteva Status app — per-instance status line.
//
// Counterpart to the long-lived directive: this is "what I'm on right
// now" (minutes-to-hours horizon). The agent writes it via MCP; the
// dashboard reads it live.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: status
display_name: Status
version: 1.0.0
description: Per-instance status line. Agent writes via MCP; dashboard reads live.
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
    - name: status_set
      description: Set the instance's status line.
    - name: status_get
      description: Read the instance's current status line.
    - name: status_clear
      description: Clear the instance's status line.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/status
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/status.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// Tone enum mirrors the SQL CHECK constraint — keep them in sync if you
// add a new one.
var validTones = map[string]bool{
	"info": true, "working": true, "warn": true, "error": true, "success": true, "idle": true,
}

type Status struct {
	InstanceID   int64  `json:"instance_id"`
	Message      string `json:"message"`
	Emoji        string `json:"emoji"`
	Tone         string `json:"tone"`
	SetByThread  string `json:"set_by_thread"`
	UpdatedAt    string `json:"updated_at"`
}

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
		return errors.New("status app requires a db block")
	}
	ctx.Logger().Info("status app mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

// HTTPRoutes — REST surface for the panel. Reverse-proxied at
// /api/apps/status/* by apteva-server.
//
// One pattern, internal method dispatch (Go's ServeMux refuses
// duplicate registrations).
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/instances/", Handler: a.handleInstancesItem},
	}
}

func (a *App) handleInstancesItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleGet(w, r)
	case http.MethodDelete:
		a.handleClear(w, r)
	default:
		http.Error(w, "GET or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "status_set",
			Description: "Set the instance's status line. Args: instance_id, message, emoji (optional), tone (info|working|warn|error|success|idle, default info), thread_id (optional).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instance_id": map[string]any{"type": "integer"},
					"message":     map[string]any{"type": "string"},
					"emoji":       map[string]any{"type": "string"},
					"tone":        map[string]any{"type": "string", "enum": []string{"info", "working", "warn", "error", "success", "idle"}},
					"thread_id":   map[string]any{"type": "string"},
				},
				"required": []string{"instance_id", "message"},
			},
			Handler: a.toolSet,
		},
		{
			Name:        "status_get",
			Description: "Read the instance's current status line. Args: instance_id.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"instance_id": map[string]any{"type": "integer"}},
				"required":   []string{"instance_id"},
			},
			Handler: a.toolGet,
		},
		{
			Name:        "status_clear",
			Description: "Clear the instance's status line. Args: instance_id.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"instance_id": map[string]any{"type": "integer"}},
				"required":   []string{"instance_id"},
			},
			Handler: a.toolClear,
		},
	}
}

func (a *App) Channels() []sdk.ChannelFactory       { return nil }
func (a *App) Workers() []sdk.Worker                { return nil }
func (a *App) EventHandlers() []sdk.EventHandler    { return nil }

// --- DB helpers --------------------------------------------------------------

func dbUpsert(db *sql.DB, instanceID int64, message, emoji, tone, thread string) (*Status, error) {
	if tone == "" {
		tone = "info"
	}
	if !validTones[tone] {
		return nil, fmt.Errorf("invalid tone %q", tone)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO status_status (instance_id, message, emoji, tone, set_by_thread, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
			message = excluded.message,
			emoji = excluded.emoji,
			tone = excluded.tone,
			set_by_thread = excluded.set_by_thread,
			updated_at = excluded.updated_at`,
		instanceID, message, emoji, tone, thread, now)
	if err != nil {
		return nil, err
	}
	return &Status{InstanceID: instanceID, Message: message, Emoji: emoji, Tone: tone, SetByThread: thread, UpdatedAt: now}, nil
}

func dbRead(db *sql.DB, instanceID int64) (*Status, error) {
	var s Status
	var thread sql.NullString
	err := db.QueryRow(`
		SELECT instance_id, message, COALESCE(emoji,''), tone, set_by_thread, updated_at
		FROM status_status WHERE instance_id = ?`,
		instanceID).Scan(&s.InstanceID, &s.Message, &s.Emoji, &s.Tone, &thread, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.SetByThread = thread.String
	return &s, nil
}

func dbDelete(db *sql.DB, instanceID int64) error {
	_, err := db.Exec(`DELETE FROM status_status WHERE instance_id = ?`, instanceID)
	return err
}

// --- HTTP handlers ----------------------------------------------------------

func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	id := pathSuffixInt(r.URL.Path, "/instances/")
	s, err := dbRead(globalCtx.AppDB(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, s)
}

func (a *App) handleClear(w http.ResponseWriter, r *http.Request) {
	id := pathSuffixInt(r.URL.Path, "/instances/")
	if err := dbDelete(globalCtx.AppDB(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- MCP tool handlers ------------------------------------------------------

func (a *App) toolSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["instance_id"])
	message, _ := args["message"].(string)
	emoji, _ := args["emoji"].(string)
	tone, _ := args["tone"].(string)
	thread, _ := args["thread_id"].(string)
	if id == 0 || message == "" {
		return nil, errors.New("instance_id and message required")
	}
	return dbUpsert(ctx.AppDB(), id, message, emoji, tone, thread)
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["instance_id"])
	if id == 0 {
		return nil, errors.New("instance_id required")
	}
	s, err := dbRead(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return map[string]any{"instance_id": id, "message": ""}, nil
	}
	return s, nil
}

func (a *App) toolClear(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["instance_id"])
	if id == 0 {
		return nil, errors.New("instance_id required")
	}
	if err := dbDelete(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	return map[string]any{"status": "cleared", "instance_id": id}, nil
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
		var n int64
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}

func pathSuffixInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	var n int64
	fmt.Sscanf(rest, "%d", &n)
	return n
}

var globalCtx *sdk.AppCtx

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest             { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error      { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error      { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route            { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool               { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory     { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker              { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler  { return w.app.EventHandlers() }

func main() { sdk.Run(&wrapApp{app: &App{}}) }
