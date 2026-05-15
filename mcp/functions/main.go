// Functions v1.0 — Lambda-style serverless functions.
//
// A function is an immutable, built version (functions_deploy) served
// by a pool of warm worker processes (pool.go / worker.go). The
// runtime boots once, imports the handler module, then serves
// invocations over a socketpair — no per-request process spawn.
// Handlers export `default async (event, context) => result`;
// context.call reaches other Apteva apps through the sidecar.
//
// Triggers:
//   - HTTP   — POST /fn/<name> (auto-routed, gateway-reachable).
//   - Cron   — pair with the Jobs app: jobs_schedule with
//              target={kind:http, app:"functions", path:"/fn/<name>"}.
//   - Manual — functions_invoke MCP tool.
//
// Deferred post-v1.0: the python runtime, max_memory_mb enforcement,
// and a per-function context.call allowlist.
package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// nodeHarness / goHarness are the worker harnesses, embedded so the
// running binary is self-contained. ensureBuilt stages the right one
// into a version's build dir: node.mjs runs as the node entrypoint;
// gomain.txt is written as harness.go and compiled into the worker.
//
//go:embed harness/node.mjs
var nodeHarness []byte

//go:embed harness/gomain.txt
var goHarness []byte

// examplesFS exposes the on-disk examples/ dir so the panel can list
// + load real working handlers via GET /examples. The compiler fails
// the build if a glob matches no files, so a missing example file
// surfaces as a build error rather than a runtime 404.
//
//go:embed examples/*.mjs examples/*.go.txt
var examplesFS embed.FS

// ─── Manifest (also lives in apteva.yaml; embedded so the running
// binary is self-describing). ─────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: functions
display_name: Functions
version: 1.2.0
description: |
  Lambda-style serverless functions in node or Go. Each function is
  an immutable, built version served by a pool of warm worker
  processes; handlers reach other apps via context.call. Auto-routed
  HTTP endpoint at /fn/<name>.
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
      description: Create a function and deploy its first version.
    - name: functions_update
      description: Update a function's metadata (env, limits, status).
    - name: functions_delete
      description: Delete a function and all its versions + invocations.
    - name: functions_list
      description: List functions in the project.
    - name: functions_get
      description: Fetch one function by id or name.
    - name: functions_invoke
      description: Synchronously invoke a function's active version.
    - name: functions_invocations
      description: Recent invocations for a function.
    - name: functions_logs
      description: Return value + console output of a single invocation.
    - name: functions_deploy
      description: Deploy a new immutable version and make it active.
    - name: functions_rollback
      description: Make an older built version active again.
    - name: functions_versions
      description: List a function's deploy history.
  ui_panels:
    - slot: project.page
      label: Functions
      icon: code
      entry: /ui/FunctionsPanel.mjs
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
	p, err := newPool(ctx)
	if err != nil {
		return fmt.Errorf("init worker pool: %w", err)
	}
	globalPool = p
	ctx.Logger().Info("functions mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if globalPool != nil {
		globalPool.shutdown()
		globalPool = nil
	}
	return nil
}
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
		// Built-in handler examples for the panel's "Load" picker.
		{Pattern: "/examples", Handler: a.handleHTTPExamples},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "functions_create",
			Description: "Create a function and deploy v1. Args: name, runtime (node|go), source (inline handler — node: `export default async (event, context) => result`; go: `func Handle(event json.RawMessage, ctx *Context) (any, error)`) OR (repo_id+repo_path), package_json?, env?, timeout_ms?, max_memory_mb?.",
			InputSchema: schemaObject(map[string]any{
				"name":          map[string]any{"type": "string"},
				"runtime":       map[string]any{"type": "string", "enum": []any{"node", "go"}},
				"source_kind":   map[string]any{"type": "string", "enum": []any{"inline", "repo"}},
				"source":        map[string]any{"type": "string", "description": "Inline handler module body (when source_kind=inline)."},
				"repo_id":       map[string]any{"type": "integer", "description": "Code app repo id (when source_kind=repo)."},
				"repo_path":     map[string]any{"type": "string", "description": "Entry file path within the repo."},
				"package_json":  map[string]any{"type": "string", "description": "Optional package.json — dependencies installed once at deploy."},
				"env":           map[string]any{"type": "object", "description": "String map merged into the worker env."},
				"timeout_ms":    map[string]any{"type": "integer", "description": "Hard timeout per invocation. Default 30000, max 300000."},
				"max_memory_mb": map[string]any{"type": "integer", "description": "Memory cap (MB). Default 256."},
			}, []string{"name", "runtime"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "functions_update",
			Description: "Update a function's metadata: env, timeout_ms, max_memory_mb, status. Source / runtime changes go through functions_deploy. Args: id (or name) + the fields to change.",
			InputSchema: schemaObject(map[string]any{
				"id":            map[string]any{"type": "integer"},
				"name":          map[string]any{"type": "string"},
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
			Description: "Synchronously invoke a function's active version. Args: id (or name), event (any JSON; passed to the handler).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"name":  map[string]any{"type": "string"},
				"event": map[string]any{"description": "JSON payload passed to the handler as its first argument."},
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
			Description: "Return value + captured console output of one invocation. Args: invocation_id.",
			InputSchema: schemaObject(map[string]any{
				"invocation_id": map[string]any{"type": "integer"},
			}, []string{"invocation_id"}),
			Handler: a.toolLogs,
		},
		{
			Name:        "functions_deploy",
			Description: "Deploy a new immutable version of a function and make it active. Args: id (or name), source OR (repo_id+repo_path), source_kind?, package_json?.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"name":         map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string"},
				"source_kind":  map[string]any{"type": "string", "enum": []any{"inline", "repo"}},
				"repo_id":      map[string]any{"type": "integer"},
				"repo_path":    map[string]any{"type": "string"},
				"package_json": map[string]any{"type": "string", "description": "Optional package.json — dependencies installed once at deploy."},
			}, nil),
			Handler: a.toolDeploy,
		},
		{
			Name:        "functions_rollback",
			Description: "Make an older, already-built version active again. Args: id (or name), version (the version number to roll back to).",
			InputSchema: schemaObject(map[string]any{
				"id":      map[string]any{"type": "integer"},
				"name":    map[string]any{"type": "string"},
				"version": map[string]any{"type": "integer"},
			}, []string{"version"}),
			Handler: a.toolRollback,
		},
		{
			Name:        "functions_versions",
			Description: "List a function's deploy history. Args: id (or name), limit (default 50, max 100).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"name":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolVersions,
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
