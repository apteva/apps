// Apteva Fleet — control plane for a fleet of client apteva instances.
// v0.1 spawns each tenant as a local apteva process on the parent host
// with its own --data-dir + --port. Zero cross-app deps.
package main

import (
	"errors"
	"net/http"
	"os"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: fleet
display_name: Fleet
version: 0.2.1
description: Control plane for a local fleet of apteva tenants.
author: Apteva
scopes: [global]
requires:
  permissions:
    - db.write.app
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: tenant_create
      description: Spawn a new local apteva tenant.
    - name: tenant_attach_key
      description: Finish admin-driven setup by attaching the tenant's api_key.
    - name: tenant_connect
      description: Register an existing apteva-server as a tenant.
    - name: tenant_list
      description: List managed tenants.
    - name: tenant_get
      description: Full record for one tenant.
    - name: tenant_start
      description: Start a stopped local tenant.
    - name: tenant_stop
      description: Stop a running local tenant.
    - name: tenant_delete
      description: Stop and remove a tenant.
    - name: tenant_support_login
      description: Mint a short-lived super-admin URL on the tenant.
    - name: tenant_run_remote
      description: Proxy an MCP tool call to a tenant.
  ui_panels:
    - slot: project.page
      label: Fleet
      icon: server
      entry: /ui/FleetPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/fleet
  image: ghcr.io/apteva/fleet:0.1.0
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/fleet.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct {
	store *store
	keys  *keyring

	// procs tracks PIDs of locally-spawned tenants in memory. Lost on
	// fleet restart — the OnMount reconciler reattaches by probing
	// each local tenant's port instead of trying to recover PIDs.
	procMu sync.Mutex
	procs  map[string]*tenantProc
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("fleet requires a db block")
	}
	k, err := loadKeyring(ctx)
	if err != nil {
		return err
	}
	a.keys = k
	a.store = &store{db: ctx.AppDB()}
	a.procs = map[string]*tenantProc{}
	if err := a.reconcileOnBoot(); err != nil {
		ctx.Logger().Warn("fleet: reconcile on boot", "err", err)
	}
	ctx.Logger().Info("fleet mounted", "data_root", localDataRoot())
	return nil
}

// OnUnmount is the platform's graceful-shutdown hook. We do NOT kill
// tenant children on fleet shutdown — children are spawned with their
// own process group so they survive a fleet restart. Operators stop
// tenants explicitly via tenant_stop.
func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/tenants", Handler: a.httpList},
		{Method: http.MethodGet, Pattern: "/tenants/", Handler: a.httpGet},
		{Method: http.MethodGet, Pattern: "/health", Handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "tenant_create",
			Description: "Spawn a new local apteva tenant in admin-driven setup mode. Allocates a data dir (~/.apteva-fleet/<slug>/) and free port, mints a setup token, runs the apteva CLI with --data-dir + --port + --no-browser + APTEVA_SETUP_TOKEN env, waits for /api/health. Returns status=setup_pending plus a setup_url and setup_token the operator uses to register the admin in the browser. Call tenant_attach_key afterwards with the api_key generated on the tenant dashboard. Args: slug (required), owner_email (required), apteva_bin (optional — path to apteva binary; defaults to $FLEET_APTEVA_BIN or `apteva` on PATH).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slug":        map[string]any{"type": "string"},
					"owner_email": map[string]any{"type": "string"},
					"apteva_bin":  map[string]any{"type": "string"},
				},
				"required": []string{"slug", "owner_email"},
			},
			Handler: a.toolCreate,
		},
		{
			Name: "tenant_attach_key",
			Description: "Finish admin-driven setup. After tenant_create returns status=setup_pending and the operator has (1) opened the setup URL, (2) registered an admin email + password using the setup_token, and (3) generated an api_key on the tenant dashboard — call this with the api_key to flip the tenant to active. Validates by GETing /api/auth/status with the key. Args: tenant_id, api_key.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"api_key":   map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "api_key"},
			},
			Handler: a.toolAttachKey,
		},
		{
			Name:        "tenant_connect",
			Description: "Register an existing apteva-server as a tenant. Verifies base_url + api_key against /api/health before persisting. Args: base_url, api_key, owner_email, slug?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"base_url":    map[string]any{"type": "string"},
					"api_key":     map[string]any{"type": "string"},
					"owner_email": map[string]any{"type": "string"},
					"slug":        map[string]any{"type": "string"},
				},
				"required": []string{"base_url", "api_key", "owner_email"},
			},
			Handler: a.toolConnect,
		},
		{
			Name:        "tenant_list",
			Description: "List managed tenants. Args: status?, owner_email?, version?, kind?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status":      map[string]any{"type": "string"},
					"owner_email": map[string]any{"type": "string"},
					"version":     map[string]any{"type": "string"},
					"kind":        map[string]any{"type": "string"},
				},
			},
			Handler: a.toolList,
		},
		{
			Name:        "tenant_get",
			Description: "Fetch one tenant + last 20 events. Args: tenant_id.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"tenant_id": map[string]any{"type": "string"}},
				"required":   []string{"tenant_id"},
			},
			Handler: a.toolGet,
		},
		{
			Name:        "tenant_start",
			Description: "Start a stopped local tenant. Re-spawns the apteva process at the tenant's existing port + data dir. Returns an error for remote tenants. Args: tenant_id.",
			InputSchema: idOnlySchema(),
			Handler:     a.toolStart,
		},
		{
			Name:        "tenant_stop",
			Description: "Stop a running local tenant: SIGTERM → wait 10s → SIGKILL. For remote tenants this is registry-only (marks suspended). Args: tenant_id.",
			InputSchema: idOnlySchema(),
			Handler:     a.toolStop,
		},
		{
			Name:        "tenant_delete",
			Description: "Stop and remove a tenant. For local tenants, also wipes the data dir — only when confirm=true. Args: tenant_id, confirm? (required to wipe data dir).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"confirm":   map[string]any{"type": "boolean"},
				},
				"required": []string{"tenant_id"},
			},
			Handler: a.toolDelete,
		},
		{
			Name:        "tenant_support_login",
			Description: "Mint a short-lived super-admin URL on the tenant via its POST /api/admin/support_session. Args: tenant_id, reason.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"reason":    map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "reason"},
			},
			Handler: a.toolSupportLogin,
		},
		{
			Name:        "tenant_run_remote",
			Description: "Proxy an MCP tool call to a tenant. Args: tenant_id, app (tenant-side app name), tool, input.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"app":       map[string]any{"type": "string"},
					"tool":      map[string]any{"type": "string"},
					"input":     map[string]any{"type": "object"},
				},
				"required": []string{"tenant_id", "app", "tool"},
			},
			Handler: a.toolRunRemote,
		},
	}
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "health_poller", Schedule: "@every 60s", Run: a.runHealthPoller},
	}
}

func idOnlySchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"tenant_id": map[string]any{"type": "string"}},
		"required":   []string{"tenant_id"},
	}
}

// localDataRoot returns where fleet keeps each local tenant's data dir.
// Override with FLEET_DATA_ROOT; default ~/.apteva-fleet.
func localDataRoot() string {
	if v := os.Getenv("FLEET_DATA_ROOT"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return home + "/.apteva-fleet"
}

func main() {
	sdk.Run(&App{})
}
