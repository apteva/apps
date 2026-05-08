// Functions v0.1 — Lambda-style serverless functions.
//
// One sidecar; each function defined here gets an auto-routed HTTP
// endpoint at /fn/<name>. The dispatcher spawns the configured
// runtime per invocation with stdin = event JSON, captures stdout
// as the response, kills on timeout. No long-running processes; no
// build step beyond writing the source to a temp dir.
//
// Triggers:
//   - HTTP   — POST /fn/<name> (auto-routed, gateway-reachable).
//   - Cron   — pair with the Jobs app: jobs_schedule with
//              target={kind:http, app:"functions", path:"/fn/<name>"}.
//   - Manual — functions_invoke MCP tool.
//
// No deploy integration in v0.1; functions never become long-running
// supervised releases. If you need that, use the deploy app.
package main

import (
	"errors"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml; embedded so the running
// binary is self-describing). ─────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: functions
display_name: Functions
version: 0.1.0
description: |
  Lambda-style serverless functions. Each function gets an
  auto-routed HTTP endpoint at /fn/<name>; the dispatcher spawns
  the runtime per invocation with stdin=event JSON, captures
  stdout as response, kills on timeout.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: functions_create
      description: Create a function with an inline source body or a code-app repo path.
    - name: functions_update
      description: Update a function's source, env, or limits.
    - name: functions_delete
      description: Delete a function and all its invocations.
    - name: functions_list
      description: List functions in the project.
    - name: functions_get
      description: Fetch one function by id or name.
    - name: functions_invoke
      description: Synchronously invoke a function with an event payload.
    - name: functions_invocations
      description: Recent invocations for a function.
    - name: functions_logs
      description: stdout + stderr of a single invocation.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/functions
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/functions.db
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
		return errors.New("functions requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("functions mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// CRUD on functions.
		{Pattern: "/functions", Handler: a.handleHTTPFunctionsCollection},
		{Pattern: "/functions/", Handler: a.handleHTTPFunctionItem},
		// Auto-routed invocation endpoint. /fn/<name> on the sidecar
		// becomes /api/apps/functions/fn/<name> through the gateway —
		// that's the URL the Jobs app uses for cron-fired schedules.
		{Pattern: "/fn/", Handler: a.handleHTTPInvokeByName},
		// Recent invocations across the project (dashboard).
		{Pattern: "/invocations", Handler: a.handleHTTPInvocationsCollection},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "functions_create",
			Description: "Create a function. Args: name, runtime (bun|node|python|sh), source (inline) OR (repo_id+repo_path), env?, timeout_ms?, max_memory_mb?.",
			InputSchema: schemaObject(map[string]any{
				"name":          map[string]any{"type": "string"},
				"runtime":       map[string]any{"type": "string", "enum": []any{"bun", "node", "python", "sh"}},
				"source_kind":   map[string]any{"type": "string", "enum": []any{"inline", "repo"}},
				"source":        map[string]any{"type": "string", "description": "Inline source body (when source_kind=inline)."},
				"repo_id":       map[string]any{"type": "integer", "description": "Code app repo id (when source_kind=repo)."},
				"repo_path":     map[string]any{"type": "string", "description": "Entry file path within the repo."},
				"env":           map[string]any{"type": "object", "description": "String map merged into spawn env."},
				"timeout_ms":    map[string]any{"type": "integer", "description": "Hard timeout per invocation. Default 30000, max 300000."},
				"max_memory_mb": map[string]any{"type": "integer", "description": "Soft memory cap. Default 256."},
			}, []string{"name", "runtime"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "functions_update",
			Description: "Update a function. Args: id (or name), and any field from create.",
			InputSchema: schemaObject(map[string]any{
				"id":            map[string]any{"type": "integer"},
				"name":          map[string]any{"type": "string"},
				"runtime":       map[string]any{"type": "string"},
				"source_kind":   map[string]any{"type": "string"},
				"source":        map[string]any{"type": "string"},
				"repo_id":       map[string]any{"type": "integer"},
				"repo_path":     map[string]any{"type": "string"},
				"env":           map[string]any{"type": "object"},
				"timeout_ms":    map[string]any{"type": "integer"},
				"max_memory_mb": map[string]any{"type": "integer"},
				"status":        map[string]any{"type": "string", "enum": []any{"active", "disabled"}},
			}, nil),
			Handler: a.toolUpdate,
		},
		{
			Name:        "functions_delete",
			Description: "Delete a function and all its invocation rows.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolDelete,
		},
		{
			Name:        "functions_list",
			Description: "List functions. Args: runtime?, status?, limit (default 100, max 500).",
			InputSchema: schemaObject(map[string]any{
				"runtime": map[string]any{"type": "string"},
				"status":  map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "functions_get",
			Description: "Fetch a function by id or name.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolGet,
		},
		{
			Name:        "functions_invoke",
			Description: "Synchronously invoke a function. Args: id (or name), event (any JSON; passed via stdin).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"name":  map[string]any{"type": "string"},
				"event": map[string]any{"description": "JSON payload passed to the function via stdin."},
			}, nil),
			Handler: a.toolInvoke,
		},
		{
			Name:        "functions_invocations",
			Description: "List recent invocations of a function. Args: id (or name), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"name":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolInvocations,
		},
		{
			Name:        "functions_logs",
			Description: "stdout + stderr of one invocation. Args: invocation_id.",
			InputSchema: schemaObject(map[string]any{
				"invocation_id": map[string]any{"type": "integer"},
			}, []string{"invocation_id"}),
			Handler: a.toolLogs,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────
//
// Same pattern as jobs/crm. scope=project installs read
// APTEVA_PROJECT_ID from env; scope=global installs require the
// caller to pass _project_id (MCP) or ?project_id (HTTP).

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if raw, has := args["_project_id"]; has {
		if v, ok := raw.(string); ok {
			return v, nil
		}
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

// globalCtx holds the AppCtx the SDK threaded through OnMount, so
// HTTP handlers (which the SDK doesn't yet expose a context hook
// for) can reach the DB and platform client. Single-write at boot,
// many-read after — no mutex needed.
var globalCtx *sdk.AppCtx
