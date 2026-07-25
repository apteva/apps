// Workflows — deterministic, auditable pipelines.
//
// A workflow is a YAML/JSON definition: a list of typed steps
// (http, function, app, emit, branch) chained linearly with goto-
// style branching. Manual, HTTP, and app-event triggers execute
// RunWorkflow, which walks the steps and records a per-step audit row.
//
// Strict separation from agents: workflows never call LLMs. If a
// step needs judgment, the workflow emits an event and the agent
// handles it; a downstream workflow picks up the agent's reply.
//
// Scheduled triggers still belong in the Jobs app. Wait-on-event and
// parallel/fan-out steps remain out of scope.
package main

import (
	"errors"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest. ────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: workflows
display_name: Workflows
version: 0.4.7
description: |
  Deterministic, on-demand pipelines. A workflow is a YAML/JSON
  graph of typed steps (http, function, app, integration, emit,
  branch) with event triggers, goto-style branching and retry.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
min_apteva_version: "0.18.0"
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.connections.execute
  dynamic_app_calls: true
  dynamic_integration_access: true
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: workflows_create
      description: Create a workflow from inline source or a code-app repo path.
    - name: workflows_update
      description: Update a workflow's source, trigger, or status.
    - name: workflows_delete
      description: Delete a workflow and cascade-drop its runs.
    - name: workflows_list
      description: List workflows in the project.
    - name: workflows_get
      description: Fetch one workflow by id or name.
    - name: workflows_run
      description: Synchronously execute a workflow with an input payload.
    - name: workflows_runs
      description: Recent runs for a workflow.
    - name: workflows_run_status
      description: Full run + step trace.
    - name: workflows_replay
      description: Re-run a past run, optionally skipping ahead to a specific step.
    - name: workflows_cancel
      description: Cancel an in-flight run.
  publishes:
    - { name: workflow.created, description: "A workflow was created." }
    - { name: workflow.updated, description: "A workflow was updated." }
    - { name: workflow.deleted, description: "A workflow was deleted." }
    - { name: workflow.step.started, description: "A workflow step started." }
    - { name: workflow.step.completed, description: "A workflow step completed." }
    - { name: workflow.run.finished, description: "A workflow run reached a terminal state." }
    - { name: workflow.run.cancelled, description: "Cancellation was requested for a workflow run." }
  ui_panels:
    - slot: project.page
      label: Workflows
      icon: git-branch
      entry: /ui/WorkflowsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/workflows
  port: 8080
  health_check: /subscriber/health
  resources:
    cpu: 0.1
    memory: 96
    cpu_limit: 1
    memory_limit: 256
  storage:
    - name: data
      mount_path: /data
  env:
    APTEVA_GATEWAY_URL: { from: platform }
    APTEVA_APP_TOKEN:   { from: platform }
    APTEVA_INSTALL_ID:  { from: platform }
    APTEVA_PROJECT_ID:  { from: platform }
    APTEVA_APP_CONFIG:  { from: platform }
db:
  driver: sqlite
  path: /data/workflows.db
  migrations: migrations/
config_schema: []
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
		return errors.New("workflows requires a db block")
	}
	globalCtx = ctx
	// Sweep stuck runs from a previous boot. Synchronous v0.1
	// doesn't support resume; runs in flight when the sidecar
	// crashed are unsalvageable, so mark them failed and move on.
	if err := dbSweepStuckRuns(ctx.AppDB()); err != nil {
		ctx.Logger().Warn("sweep stuck runs", "err", err)
	}
	// Start the event-trigger manager: opens one SSE per
	// (source_app, project) lane for every active workflow whose
	// trigger.kind=event. Missing platform configuration is exposed
	// as degraded readiness and in subscriber diagnostics.
	globalEventTrigger = newEventTrigger(ctx)
	globalEventTrigger.Start()
	ctx.Logger().Info("workflows mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if globalEventTrigger != nil {
		globalEventTrigger.Stop()
		globalEventTrigger = nil
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/workflows", Handler: a.handleHTTPWorkflowsCollection},
		{Pattern: "/workflows/", Handler: a.handleHTTPWorkflowItem},
		// Auto-routed trigger. /wf/<name> on the sidecar becomes
		// /api/apps/workflows/wf/<name> through the gateway — the
		// URL jobs would target for cron-fired runs.
		{Pattern: "/wf/", Handler: a.handleHTTPRunByName},
		// Run inspection. Not project-prefixed because run ids are
		// already project-checked at lookup.
		{Pattern: "/runs", Handler: a.handleHTTPRunsCollection},
		{Pattern: "/runs/", Handler: a.handleHTTPRunItem},
		{Method: http.MethodGet, Pattern: "/subscriber/status", Handler: a.handleSubscriberStatus},
		// The platform supervisor probes without a sidecar bearer. This
		// response contains no credentials and the process binds loopback.
		{Method: http.MethodGet, Pattern: "/subscriber/health", Handler: a.handleSubscriberHealth, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "workflows_create",
			Description: "Create a workflow. Args: name, source (YAML/JSON inline) OR (repo_id+repo_path), trigger?, status?.",
			InputSchema: schemaObject(map[string]any{
				"name":         map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string", "description": "Inline YAML or JSON definition."},
				"source_kind":  map[string]any{"type": "string", "enum": []any{"inline", "repo"}},
				"repo_id":      map[string]any{"type": "integer"},
				"repo_path":    map[string]any{"type": "string"},
				"trigger_kind": map[string]any{"type": "string", "enum": []any{"http", "manual", "event"}},
				"trigger_json": map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string", "enum": []any{"active", "disabled"}},
			}, []string{"name"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "workflows_update",
			Description: "Update a workflow. Args: id (or name), and any field from create.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"name":         map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string"},
				"source_kind":  map[string]any{"type": "string"},
				"repo_id":      map[string]any{"type": "integer"},
				"repo_path":    map[string]any{"type": "string"},
				"trigger_kind": map[string]any{"type": "string"},
				"trigger_json": map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolUpdate,
		},
		{
			Name:        "workflows_delete",
			Description: "Delete a workflow and all its runs.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolDelete,
		},
		{
			Name:        "workflows_list",
			Description: "List workflows. Args: status?, trigger_kind?, limit (default 100, max 500).",
			InputSchema: schemaObject(map[string]any{
				"status":       map[string]any{"type": "string"},
				"trigger_kind": map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "workflows_get",
			Description: "Fetch a workflow by id or name.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolGet,
		},
		{
			Name:        "workflows_run",
			Description: "Synchronously run a workflow. Args: id (or name), input (any JSON).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"name":  map[string]any{"type": "string"},
				"input": map[string]any{"description": "Trigger payload, available as {{ input.* }} in steps."},
			}, nil),
			Handler: a.toolRun,
		},
		{
			Name:        "workflows_runs",
			Description: "List recent runs for a workflow. Args: id (or name), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"name":  map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolRuns,
		},
		{
			Name:        "workflows_run_status",
			Description: "Full run + step trace. Args: run_id.",
			InputSchema: schemaObject(map[string]any{
				"run_id": map[string]any{"type": "integer"},
			}, []string{"run_id"}),
			Handler: a.toolRunStatus,
		},
		{
			Name:        "workflows_replay",
			Description: "Re-run a past run with the same input. Args: run_id, from_step? (skip ahead to this step id).",
			InputSchema: schemaObject(map[string]any{
				"run_id":    map[string]any{"type": "integer"},
				"from_step": map[string]any{"type": "string"},
			}, []string{"run_id"}),
			Handler: a.toolReplay,
		},
		{
			Name:        "workflows_cancel",
			Description: "Cancel an in-flight run. Args: run_id.",
			InputSchema: schemaObject(map[string]any{
				"run_id": map[string]any{"type": "integer"},
			}, []string{"run_id"}),
			Handler: a.toolCancel,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────

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

// globalCtx mirrors the pattern in jobs / functions: HTTP handlers
// don't get an AppCtx threaded by the SDK, so we capture it at
// OnMount and use it in handlers.go.
var globalCtx *sdk.AppCtx

// globalEventTrigger lives next to globalCtx for the same reason:
// HTTP and MCP handlers reach it after create/update/delete to
// Kick() a reconcile so newly-added event triggers go live
// immediately instead of waiting for the periodic rescan.
var globalEventTrigger *eventTrigger
