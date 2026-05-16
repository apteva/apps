// Apteva Fleet — control plane for a fleet of client apteva instances.
// v0.1 spawns each tenant as a local apteva process on the parent host
// with its own --data-dir + --port. Zero cross-app deps.
package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: fleet
display_name: Fleet
version: 0.5.0
description: Control plane for a local fleet of apteva tenants.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
  integrations:
    - role: domains
      kind: app
      required: false
      compatible_app_names: [domains]
      label: Domains app
      hint: Install the Domains app to attach custom hostnames to tenants.
    - role: certs
      kind: app
      required: false
      compatible_app_names: [certs]
      label: Certs app
      hint: Install the Certs app to auto-issue Let's Encrypt certs on attach.
    - role: routes
      kind: app
      required: false
      compatible_app_names: [routes]
      label: Routes app
      hint: Install the Routes app to publish tenants at public hostnames via the parent's reverse proxy.
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
    - name: tenant_attach_domain
      description: Attach a public hostname to a tenant via the Domains/Certs/Routes apps.
    - name: tenant_detach_domain
      description: Clear a tenant's domain link (DNS delete, cert revoke, route unregister).
    - name: tenant_update
      description: Update a tenant's apteva version. Installs the requested version into a fleet-owned npm prefix, then respawns.
    - name: tenant_check_updates
      description: Report npm's apteva latest version + which tenants are behind. Read-only.
    - name: tenant_set_target_version
      description: Pin a tenant's desired apteva version without applying. Surfaces drift on the panel.
    - name: tenant_reveal_api_key
      description: Return the tenant's api_key (unsealed from fleet's keyring).
    - name: tenant_reset_admin_password
      description: Rotate the tenant admin user's password to a fresh random one. Revokes all existing sessions for that user. Returns the new password.
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

	// publicHost is the host name shown to operators in API responses
	// and the panel. Determined once at OnMount via detectPublicHost
	// (UDP-dial trick to 8.8.8.8 reads back the outbound interface IP),
	// then frozen for the process lifetime — we don't expect the host's
	// outbound interface to change at runtime. Falls back to "localhost"
	// when network detection fails (offline dev box, locked-down VPS).
	publicHost string
}

// globalCtx captures the platform context at OnMount so HTTP handlers
// (which don't get a ctx parameter from the SDK) can issue cross-app
// calls. Same pattern deploy uses.
var globalCtx *sdk.AppCtx

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
	a.publicHost = detectPublicHost()
	globalCtx = ctx
	if err := a.reconcileOnBoot(); err != nil {
		ctx.Logger().Warn("fleet: reconcile on boot", "err", err)
	}
	ctx.Logger().Info("fleet mounted", "data_root", localDataRoot(), "public_host", a.publicHost)
	return nil
}

// OnUnmount is the platform's graceful-shutdown hook. We do NOT kill
// tenant children on fleet shutdown — children are spawned with their
// own process group so they survive a fleet restart. Operators stop
// tenants explicitly via tenant_stop.
func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	// /health is registered by the SDK framework itself (see app-sdk
	// run.go); declaring another here panics on duplicate ServeMux
	// pattern registration.
	//
	// /tenants/ is a tail-prefix route; we dispatch on the sub-path
	// inside httpTenantItem so /tenants/<id>, /tenants/<id>/attach-domain,
	// and /tenants/<id>/update can all share one ServeMux pattern.
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/tenants", Handler: a.httpList},
		{Pattern: "/tenants/", Handler: a.httpTenantItem},
		{Method: http.MethodGet, Pattern: "/_meta", Handler: a.httpMeta},
	}
}

// httpTenantItem dispatches /tenants/<id>[/sub] to the right handler.
// Putting sub-routes under one ServeMux entry avoids the SDK's "one
// pattern per route" friction without giving up clean URLs.
func (a *App) httpTenantItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/tenants/")
	id, sub, _ := strings.Cut(rest, "/")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, errors.New("tenant id required"))
		return
	}
	switch sub {
	case "":
		a.httpGet(w, r)
	case "attach-domain":
		a.httpAttachDomain(w, r)
	case "detach-domain":
		a.httpDetachDomain(w, r)
	case "update":
		a.httpUpdate(w, r)
	case "reveal-api-key":
		a.httpRevealAPIKey(w, r)
	case "reset-admin-password":
		a.httpResetAdminPassword(w, r)
	default:
		writeJSONErr(w, http.StatusNotFound, errors.New("no such sub-resource: "+sub))
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "tenant_create",
			Description: "Spawn a new local apteva tenant in admin-driven setup mode. Allocates a data dir (~/.apteva-fleet/<slug>/) and free port, mints a setup token, runs the apteva CLI with --data-dir + --port + --no-browser, waits for /api/health. Returns status=active (auto-setup happy path) or setup_pending plus a setup_url and setup_token the operator uses to register the admin in the browser. v0.4 default: pins the new tenant to npm's apteva@latest (installed into ~/.apteva-fleet/versions/<v>/ once, then re-used). Override with apteva_version (e.g. \"0.17.0\", \"latest\", or \"host\" to use whatever's on PATH); FLEET_DEFAULT_APTEVA_VERSION env sets the fleet-wide default. Args: slug (required), owner_email (required), apteva_version (optional, default \"latest\"), apteva_bin (optional — literal binary path; bypasses version resolution).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slug":            map[string]any{"type": "string"},
					"owner_email":     map[string]any{"type": "string"},
					"apteva_version":  map[string]any{"type": "string"},
					"apteva_bin":      map[string]any{"type": "string"},
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
		{
			Name:        "tenant_attach_domain",
			Description: "Attach a public hostname to a tenant. Orchestrates: Domains app writes the DNS record → Certs app issues a Let's Encrypt cert via DNS-01 → Routes app registers (fqdn → tenant apteva-server port) on the parent's routes table. Idempotent; partial-failure tolerant. Args: tenant_id, fqdn, target? (defaults to fleet's public_host), type? (A or CNAME; inferred from target), ttl?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"fqdn":      map[string]any{"type": "string"},
					"target":    map[string]any{"type": "string"},
					"type":      map[string]any{"type": "string"},
					"ttl":       map[string]any{"type": "integer"},
				},
				"required": []string{"tenant_id", "fqdn"},
			},
			Handler: a.toolAttachDomain,
		},
		{
			Name:        "tenant_detach_domain",
			Description: "Clear a tenant's domain link: best-effort DNS record delete (via Domains app), cert revoke (Certs app), route unregister (Routes app), and local clear. Local clear runs even on remote failure. Args: tenant_id.",
			InputSchema: idOnlySchema(),
			Handler:     a.toolDetachDomain,
		},
		{
			Name:        "tenant_update",
			Description: "Update a tenant's apteva version. Resolves the version (npm latest if omitted), installs into a fleet-owned npm prefix at ~/.apteva-fleet/versions/<v>/, records target_version, stops the running tenant, respawns with the new binary. Other tenants are unaffected. Local-only. Args: tenant_id, version?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"version":   map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id"},
			},
			Handler: a.toolUpdate,
		},
		{
			Name:        "tenant_check_updates",
			Description: "Read-only: return npm's apteva@latest plus every local tenant whose current_version is behind. Args: (none).",
			InputSchema: map[string]any{"type": "object"},
			Handler:     a.toolCheckUpdates,
		},
		{
			Name:        "tenant_set_target_version",
			Description: "Pin a tenant's desired apteva version without applying — the panel surfaces drift between current and target. Pass empty version to clear. Args: tenant_id, version.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"version":   map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "version"},
			},
			Handler: a.toolSetTargetVersion,
		},
		{
			Name:        "tenant_reveal_api_key",
			Description: "Return the tenant's api_key. Fleet keeps the key sealed with its own keyring (AES-GCM); this tool unseals + returns. Sensitive — records an api_key_revealed event on the tenant. Args: tenant_id.",
			InputSchema: idOnlySchema(),
			Handler:     a.toolRevealAPIKey,
		},
		{
			Name:        "tenant_reset_admin_password",
			Description: "Rotate the tenant admin user's password to a fresh random value via PATCH /api/users/<id>/password on the tenant (auth'd with the stored api_key). Revokes every existing session for that user. Returns admin_email + admin_password. Use this when the operator needs admin credentials again — fleet does not persist the original password. Args: tenant_id.",
			InputSchema: idOnlySchema(),
			Handler:     a.toolResetAdminPassword,
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
