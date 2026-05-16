// Workflows v0.1 — deterministic, on-demand pipelines.
//
// A workflow is a YAML/JSON definition: a list of typed steps
// (http, function, app, emit, branch) chained linearly with goto-
// style branching. v0.1 runs synchronously: an HTTP trigger or
// the workflows_run MCP tool kicks off RunWorkflow inline, which
// walks the steps, records a per-step audit row, and returns the
// finished Run.
//
// Strict separation from agents: workflows never call LLMs. If a
// step needs judgment, the workflow emits an event and the agent
// handles it; a downstream workflow picks up the agent's reply.
//
// Out of scope for v0.1: event triggers, scheduled triggers (use
// the Jobs app instead), wait-on-event step, parallel/fan-out,
// integration step kind. All on the v0.2 list.
package main

import (
	"errors"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest. ────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: workflows
display_name: Workflows
version: 0.2.1
description: |
  Deterministic, on-demand pipelines. A workflow is a YAML/JSON
  graph of typed steps (http, function, app, emit, branch) with
  goto-style branching and per-step retry. Synchronous in v0.1.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.connections.execute
  dynamic_app_calls: true
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
  health_check: /health
db:
  driver: sqlite
  path: /data/workflows.db
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
		return errors.New("workflows requires a db block")
	}
	globalCtx = ctx
	// Sweep stuck runs from a previous boot. Synchronous v0.1
	// doesn't support resume; runs in flight when the sidecar
	// crashed are unsalvageable, so mark them failed and move on.
	if err := dbSweepStuckRuns(ctx.AppDB()); err != nil {
		ctx.Logger().Warn("sweep stuck runs", "err", err)
	}
	ctx.Logger().Info("workflows mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
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
				"trigger_kind": map[string]any{"type": "string", "enum": []any{"http", "manual", "event", "schedule"}},
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
