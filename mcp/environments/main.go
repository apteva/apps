package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Keep in sync with apteva.yaml. The source installer reads the file; the
// running sidecar uses this copy for its live manifest endpoint.
const manifestYAML = `schema: apteva-app/v1
name: environments
display_name: Environments
version: 0.3.1
description: Isolated test environments with project apps, fake connections, deterministic seeds, agents, interactive web fixtures, edge policies, and snapshots.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/environments
tags: [environments, testing, agents, evals, mocks]
scopes: [project]
min_apteva_version: "0.18.0"
requires:
  permissions: [db.write.app, platform.runtimes.read, platform.runtimes.call, platform.runtimes.manage, platform.runtime_catalog.read, platform.connections.read]
provides:
  http_routes: [{ prefix: /fixtures/, no_auth: true }, { prefix: / }]
  mcp_tools:
    - { name: environment_list, description: "List environment definitions and active runs." }
    - { name: environment_get, description: "Get one environment definition and live runtime state." }
    - { name: environment_create, description: "Create a durable environment definition." }
    - { name: environment_update, description: "Update a durable environment definition." }
    - { name: environment_delete, description: "Delete a stopped environment definition." }
    - { name: environment_start, description: "Start a defined environment and execute its seed plan." }
    - { name: environment_stop, description: "Stop an environment and destroy its runtime." }
    - { name: environment_run_create, description: "Create an inline ephemeral run for an eval or one-off test." }
    - { name: environment_run_get, description: "Get a run and its live runtime state." }
    - { name: environment_run_stop, description: "Stop an inline or definition-backed run." }
    - { name: environment_catalog, description: "List project apps, connections, fake integrations, web fixtures, agents, and snapshots." }
    - { name: environment_seed, description: "Call a runtime app tool to seed or mutate test state." }
    - { name: environment_call, description: "Call any tool on an app cloned into a run." }
    - { name: environment_inspect, description: "Inspect runtime apps, agents, edge calls, and telemetry." }
    - { name: environment_assert, description: "Evaluate app, edge, telemetry, web-state, or web-event assertions." }
    - { name: environment_snapshot, description: "Capture a reusable snapshot of a running environment." }
    - { name: environment_snapshot_list, description: "List snapshots owned by this Environments install." }
    - { name: environment_snapshot_delete, description: "Delete a snapshot." }
    - { name: environment_agent_spawn, description: "Spawn an agent inside a running environment." }
    - { name: environment_agent_send, description: "Send a message to a runtime agent." }
    - { name: environment_agent_control, description: "Pause, resume, or stop a runtime agent." }
    - { name: environment_agent_wait, description: "Wait for a runtime agent and return its normalized trace and metrics." }
  publishes:
    - { name: environment.created, description: "An environment definition was created." }
    - { name: environment.started, description: "An environment runtime is running." }
    - { name: environment.stopped, description: "An environment runtime stopped." }
    - { name: environment.failed, description: "Environment startup or reconciliation failed." }
    - { name: environment.expired, description: "A runtime disappeared or reached its TTL." }
    - { name: snapshot.created, description: "A reusable environment snapshot was captured." }
  ui_panels: [{ slot: project.page, label: "Environments", icon: boxes, entry: /ui/EnvironmentsPanel.mjs }]
  workers: [{ name: reconcile, schedule: "@every 15s" }]
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: environments/v0.3.1, entry: mcp/environments }
  port: 8080
  health_check: /health
db: { driver: sqlite, path: /data/environments.db, migrations: migrations/ }
upgrade_policy: auto-patch
`

type App struct{ svc *service }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("environments requires a database")
	}
	if ctx.RuntimeAPI() == nil {
		return errors.New("platform runtime API unavailable")
	}
	a.svc = &service{ctx: ctx, db: store{ctx.AppDB()}}
	ctx.Logger().Info("environments mounted", "project_id", ctx.CurrentProject(), "data_dir", ctx.DataDir())
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "reconcile", Schedule: "@every 15s", Run: func(ctx context.Context, app *sdk.AppCtx) error { a.svc.ctx = app; return a.svc.reconcile(ctx) }}}
}
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/fixtures/", Handler: a.handleFixture, NoAuth: true}, {Pattern: "/api/environments", Handler: a.handleEnvironments}, {Pattern: "/api/environments/", Handler: a.handleEnvironment}, {Pattern: "/api/runs", Handler: a.handleRuns}, {Pattern: "/api/runs/", Handler: a.handleRun}, {Pattern: "/api/catalog", Handler: a.handleCatalog}, {Pattern: "/api/catalog/", Handler: a.handleCatalogItem}, {Pattern: "/api/snapshots", Handler: a.handleSnapshots}, {Pattern: "/api/snapshots/", Handler: a.handleSnapshot}, {Pattern: "/api/import/legacy", Handler: a.handleLegacyImport}}
}
func main() { sdk.Run(&App{}) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
