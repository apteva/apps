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
version: 0.8.13
description: Control plane for a local fleet of apteva tenants.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.connections.execute
    - platform.ingress.write
  integrations:
    - role: domains
      kind: app
      required: false
      compatible_app_names: [domains]
      label: Domains app
      hint: Install the Domains app to attach custom hostnames to tenants.
    - role: host_provider
      kind: app
      required: false
      compatible_app_names: [instances]
      label: Instances app
      hint: Install the Instances app to host tenants on remote VPSes; without it, all tenants live on the parent host.
    - role: backup
      kind: app
      required: false
      compatible_app_names: [backup]
      label: Backup app
      hint: Install the Backup app to schedule and retain per-tenant snapshots.
provides:
  http_routes:
    - prefix: /
    - prefix: /transfers/
      no_auth: true
    - prefix: /provider-grants/
      no_auth: true
  mcp_tools:
    - name: tenant_create
      description: Spawn a new local apteva tenant.
    - name: tenant_clone
      description: Clone a Fleet tenant to local or an Instances host without stopping or modifying the source.
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
    - name: tenant_inventory
      description: Read tenant-local platform inventory through the tenant API.
    - name: tenant_platform_call
      description: Generic allowlisted tenant platform operation.
    - name: tenant_app_tools
      description: List MCP tools exposed by an installed tenant app.
    - name: tenant_app_call
      description: Call an MCP tool exposed by an installed tenant app.
    - name: tenant_attach_domain
      description: Attach a public hostname to a tenant via Domains DNS and server-native ingress.
    - name: tenant_detach_domain
      description: Clear a tenant's domain link.
    - name: tenant_domain_grant
      description: Delegate a base domain to a tenant for tenant-local apps.
    - name: tenant_domain_list
      description: List Fleet domain grants.
    - name: tenant_domain_revoke
      description: Revoke a Fleet domain grant.
    - name: tenant_domain_record_set
      description: Proxy a DNS upsert for a tenant inherited domain.
    - name: tenant_domain_record_delete
      description: Proxy a DNS delete for a tenant inherited domain.
    - name: tenant_provider_grant
      description: Expose a parent integration connection inside a tenant as a tenant-local virtual connection.
    - name: tenant_provider_grant_list
      description: List delegated provider/integration grants.
    - name: tenant_provider_grant_revoke
      description: Revoke a delegated provider/integration grant.
    - name: tenant_migrate
      description: Move a Fleet tenant between local and Instances hosts — cold transfer of the data dir, re-spawn there, re-point the route.
    - name: tenant_migrate_finalize
      description: Permanently remove a source retained by tenant_migrate after explicit confirmation.
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
    - name: fleet_tenant_backup_plan
      description: Report backup coverage and Backup-app scope arguments for a tenant.
    - name: fleet_tenant_snapshot
      description: Provider hook used by Backup to snapshot one local Fleet tenant.
    - name: fleet_tenant_restore
      description: Provider hook used by Backup to restore one local Fleet tenant.
  ui_panels:
    - slot: project.page
      label: Fleet
      icon: server
      entry: /ui/FleetPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: fleet/v0.8.13
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
	procMu     sync.Mutex
	procs      map[string]*tenantProc
	opMu       sync.Mutex
	operations map[string]string

	// publicHost is the host name shown to operators in API responses
	// and the panel. Determined once at OnMount via detectPublicHost
	// (UDP-dial trick to 8.8.8.8 reads back the outbound interface IP),
	// then frozen for the process lifetime — we don't expect the host's
	// outbound interface to change at runtime. Falls back to "localhost"
	// when network detection fails (offline dev box, locked-down VPS).
	publicHost string

	transferMu     sync.Mutex
	transfers      map[string]*tenantTransfer
	transferSecret []byte
	metaMu         sync.Mutex
	metaCache      map[string]metaCacheEntry
	hostedTunnelMu sync.Mutex
	hostedTunnels  map[hostedTunnelKey]int
	dirtyTunnels   map[hostedTunnelKey]bool
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
	a.operations = map[string]string{}
	a.metaCache = map[string]metaCacheEntry{}
	a.hostedTunnels = map[hostedTunnelKey]int{}
	a.dirtyTunnels = map[hostedTunnelKey]bool{}
	if err := a.initTransferState(); err != nil {
		return err
	}
	a.publicHost = detectPublicHost()
	globalCtx = ctx
	if err := a.reconcileOnBoot(ctx); err != nil {
		ctx.Logger().Warn("fleet: reconcile on boot", "err", err)
	}
	ctx.Logger().Info("fleet mounted", "data_root", localDataRoot(), "public_host", a.publicHost)
	return nil
}

// OnUnmount is the platform's graceful-shutdown hook. We do NOT kill
// tenant children on fleet shutdown — children are spawned with their
// own process group so they survive a fleet restart. Operators stop
// tenants explicitly via tenant_stop.
func (a *App) OnUnmount(ctx *sdk.AppCtx) error {
	a.hostedTunnelMu.Lock()
	keys := make([]hostedTunnelKey, 0, len(a.hostedTunnels))
	for key := range a.hostedTunnels {
		keys = append(keys, key)
	}
	a.hostedTunnels = map[hostedTunnelKey]int{}
	a.dirtyTunnels = map[hostedTunnelKey]bool{}
	a.hostedTunnelMu.Unlock()
	for _, key := range keys {
		a.closeHostedTunnel(ctx, key.InstanceID, key.TargetPort)
	}
	return nil
}

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
		{Pattern: "/transfers/", Handler: a.httpTransfer, NoAuth: true},
		{Method: http.MethodPost, Pattern: "/provider-grants/", Handler: a.httpProviderGrantExecute, NoAuth: true},
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
	case "migrate":
		a.httpMigrate(w, r)
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
			Description: "Spawn a new apteva tenant. Default behavior (instance_id=0): local process on the parent host under ~/.apteva-fleet/<slug>/, pinned to npm apteva@latest. Pass instance_id>0 (a row id from the Instances app) to host the tenant on a remote VPS instead — fleet uses instance_run_command over SSH for spawn / stop / version-install / health-probe, with data living at /var/lib/apteva-fleet/<slug>/ on the VPS. Auto-setup orchestrator runs against either; on success returns admin_email + admin_password + api_key (one-shot). Args: slug (required), owner_email (required), instance_id? (default 0 = local), port? (hosted only — default 7100 + tenant-count-on-instance), apteva_version? (default \"latest\"), apteva_bin? (local override only).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slug":           map[string]any{"type": "string"},
					"owner_email":    map[string]any{"type": "string"},
					"instance_id":    map[string]any{"type": "integer"},
					"port":           map[string]any{"type": "integer"},
					"apteva_version": map[string]any{"type": "string"},
					"apteva_bin":     map[string]any{"type": "string"},
				},
				"required": []string{"slug", "owner_email"},
			},
			Handler: a.toolCreate,
		},
		{
			Name:        "tenant_clone",
			Description: "Clone a Fleet-managed tenant into a new tenant without stopping or modifying the source. Copies the source data dir to a new slug/config dir, creates a new Fleet row, clears public domain links on the clone, and optionally starts the clone on local or an Instances VPS. Args: source_tenant_id (required), slug (required), owner_email? (defaults to source), instance_id? (default source host; 0 = local parent, >0 = Instances VPS), port?, start? (default true).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source_tenant_id": map[string]any{"type": "string"},
					"slug":             map[string]any{"type": "string"},
					"owner_email":      map[string]any{"type": "string"},
					"instance_id":      map[string]any{"type": "integer"},
					"port":             map[string]any{"type": "integer"},
					"start":            map[string]any{"type": "boolean"},
				},
				"required": []string{"source_tenant_id", "slug"},
			},
			Handler: a.toolClone,
		},
		{
			Name:        "tenant_attach_key",
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
			Name:        "tenant_inventory",
			Description: "Read tenant-local platform inventory through the tenant API. Returns tenant metadata, Fleet domain/provider grants, and best-effort tenant projects/apps/agents/connections/MCP servers. Args: tenant_id, project_id?, include_users?, include_catalog?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":       map[string]any{"type": "string"},
					"project_id":      map[string]any{"type": "string"},
					"include_users":   map[string]any{"type": "boolean"},
					"include_catalog": map[string]any{"type": "boolean"},
				},
				"required": []string{"tenant_id"},
			},
			Handler: a.toolTenantInventory,
		},
		{
			Name:        "tenant_platform_call",
			Description: "Generic allowlisted tenant platform operation. Resources: apps, agents, projects, users, integrations, connections, mcp_servers. Args: tenant_id, resource, action, arguments?. Destructive actions require confirm=true inside arguments.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"resource":  map[string]any{"type": "string"},
					"action":    map[string]any{"type": "string"},
					"arguments": map[string]any{"type": "object"},
				},
				"required": []string{"tenant_id", "resource", "action"},
			},
			Handler: a.toolTenantPlatformCall,
		},
		{
			Name:        "tenant_app_tools",
			Description: "List MCP tools exposed by an installed tenant app. Use install_id to disambiguate multiple installs of the same app. Args: tenant_id, app?, install_id?, project_id?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":  map[string]any{"type": "string"},
					"app":        map[string]any{"type": "string"},
					"install_id": map[string]any{"type": "integer"},
					"project_id": map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id"},
			},
			Handler: a.toolTenantAppTools,
		},
		{
			Name:        "tenant_app_call",
			Description: "Call an MCP tool exposed by an installed tenant app. Use install_id or project_id to disambiguate multiple installs. Args: tenant_id, app, tool, arguments?, input?, install_id?, project_id?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":  map[string]any{"type": "string"},
					"app":        map[string]any{"type": "string"},
					"tool":       map[string]any{"type": "string"},
					"arguments":  map[string]any{"type": "object"},
					"input":      map[string]any{"type": "object"},
					"install_id": map[string]any{"type": "integer"},
					"project_id": map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "app", "tool"},
			},
			Handler: a.toolTenantAppCall,
		},
		{
			Name:        "tenant_attach_domain",
			Description: "Attach a public hostname to a tenant. Two modes: (1) manage_dns=true (default) — Domains app writes the DNS record, then Fleet registers server-native ingress/cert intent; needs the apex registered in the Domains catalog. (2) manage_dns=false — client already pointed DNS at this machine; Fleet skips registrar writes and only registers ingress. Idempotent; partial-failure tolerant. Args: tenant_id, fqdn, manage_dns? (default true), target? (DNS-mode only; defaults to fleet's public_host), type? (DNS-mode only; A or CNAME; inferred from target), ttl?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":  map[string]any{"type": "string"},
					"fqdn":       map[string]any{"type": "string"},
					"manage_dns": map[string]any{"type": "boolean"},
					"target":     map[string]any{"type": "string"},
					"type":       map[string]any{"type": "string"},
					"ttl":        map[string]any{"type": "integer"},
				},
				"required": []string{"tenant_id", "fqdn"},
			},
			Handler: a.toolAttachDomain,
		},
		{
			Name:        "tenant_domain_grant",
			Description: "Delegate a base domain to a tenant for tenant-local apps. Fleet writes the base + wildcard DNS records and registers parent ingress to the tenant server. Args: tenant_id, domain, target?, type? (A|CNAME), ttl?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"domain":    map[string]any{"type": "string"},
					"target":    map[string]any{"type": "string"},
					"type":      map[string]any{"type": "string"},
					"ttl":       map[string]any{"type": "integer"},
				},
				"required": []string{"tenant_id", "domain"},
			},
			Handler: a.toolDomainGrant,
		},
		{
			Name:        "tenant_domain_list",
			Description: "List Fleet domain grants. Args: tenant_id? (optional filter).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
				},
			},
			Handler: a.toolDomainGrantList,
		},
		{
			Name:        "tenant_domain_revoke",
			Description: "Revoke a Fleet domain grant: unregister parent ingress, delete DNS records, and remove the grant. Args: tenant_id, domain.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"domain":    map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "domain"},
			},
			Handler: a.toolDomainGrantRevoke,
		},
		{
			Name:        "tenant_domain_record_set",
			Description: "Proxy a DNS upsert for a tenant-owned inherited domain. Intended for the tenant Domains facade. Validates the record is under a Fleet grant, then writes through the parent Domains app. Args: tenant_id, domain, name, type, value, ttl?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"domain":    map[string]any{"type": "string"},
					"name":      map[string]any{"type": "string"},
					"type":      map[string]any{"type": "string"},
					"value":     map[string]any{"type": "string"},
					"ttl":       map[string]any{"type": "integer"},
				},
				"required": []string{"tenant_id", "domain", "name", "type", "value"},
			},
			Handler: a.toolDomainRecordSet,
		},
		{
			Name:        "tenant_domain_record_delete",
			Description: "Proxy a DNS delete for a tenant-owned inherited domain. Intended for the tenant Domains facade. Validates the record is under a Fleet grant, then deletes through the parent Domains app. Args: tenant_id, domain, name, type.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"domain":    map[string]any{"type": "string"},
					"name":      map[string]any{"type": "string"},
					"type":      map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "domain", "name", "type"},
			},
			Handler: a.toolDomainRecordDelete,
		},
		{
			Name:        "tenant_provider_grant",
			Description: "Expose a parent integration connection inside a tenant as a tenant-local virtual connection. Generic provider delegation with optional tenant app binding and constraints. Args: tenant_id, project_id, app_slug, parent_connection_id, grant_id?, name?, tenant_install_id?, tenant_role?, allowed_tools?, allowed_domains?, allowed_from?, metadata?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":            map[string]any{"type": "string"},
					"project_id":           map[string]any{"type": "string"},
					"app_slug":             map[string]any{"type": "string"},
					"parent_connection_id": map[string]any{"type": "integer"},
					"grant_id":             map[string]any{"type": "string"},
					"name":                 map[string]any{"type": "string"},
					"tenant_install_id":    map[string]any{"type": "integer"},
					"tenant_role":          map[string]any{"type": "string"},
					"allowed_tools":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"allowed_domains":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"allowed_from":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"metadata":             map[string]any{"type": "object"},
				},
				"required": []string{"tenant_id", "project_id", "app_slug", "parent_connection_id"},
			},
			Handler: a.toolProviderGrant,
		},
		{
			Name:        "tenant_provider_grant_list",
			Description: "List delegated provider/integration grants. Args: tenant_id? (optional filter).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
				},
			},
			Handler: a.toolProviderGrantList,
		},
		{
			Name:        "tenant_provider_grant_revoke",
			Description: "Revoke a delegated provider/integration grant, unbind the tenant app role when known, and delete the tenant virtual connection. Args: tenant_id, grant_id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"grant_id":  map[string]any{"type": "string"},
				},
				"required": []string{"tenant_id", "grant_id"},
			},
			Handler: a.toolProviderGrantRevoke,
		},
		{
			Name:        "tenant_migrate",
			Description: "Move a Fleet-managed tenant between the local parent host and Instances VPS hosts. Cold migration: stops the source apteva-server, transfers the data dir, boots and health-checks the target, then re-points the route. Set retain_source=true to leave the stopped source data intact until tenant_migrate_finalize. On failure before commit, the source is restarted and the row remains unchanged. Args: tenant_id, instance_id (0 = local parent, >0 = Instances row id), port?, retain_source?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":     map[string]any{"type": "string"},
					"instance_id":   map[string]any{"type": "integer"},
					"port":          map[string]any{"type": "integer"},
					"retain_source": map[string]any{"type": "boolean"},
				},
				"required": []string{"tenant_id", "instance_id"},
			},
			Handler: a.toolMigrate,
		},
		{
			Name:        "tenant_migrate_finalize",
			Description: "Inspect or permanently remove the stopped source retained by tenant_migrate. Omit confirm for a read-only preview; confirm=true permanently deletes only the recorded old host/path after verifying it is not the tenant's current location. Args: tenant_id, confirm?.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id": map[string]any{"type": "string"},
					"confirm":   map[string]any{"type": "boolean"},
				},
				"required": []string{"tenant_id"},
			},
			Handler: a.toolMigrateFinalize,
		},
		{
			Name:        "tenant_detach_domain",
			Description: "Clear a tenant's domain link: best-effort DNS record delete when Fleet owns the record, server-native ingress unregister, and local clear. Local clear runs even on remote failure. Args: tenant_id.",
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
		{
			Name:        "fleet_tenant_backup_plan",
			Description: "Report backup coverage and Backup-app scope arguments for one tenant. Args: tenant_id.",
			InputSchema: idOnlySchema(),
			Handler:     a.toolTenantBackupPlan,
		},
		{
			Name:        "fleet_tenant_snapshot",
			Description: "Provider hook used by Backup. Stops a local tenant if needed, archives its full data dir plus metadata, restarts it, and returns archive_b64. Args: tenant_id or scope_id.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":  map[string]any{"type": "string"},
					"scope_id":   map[string]any{"type": "string"},
					"scope_kind": map[string]any{"type": "string"},
				},
			},
			Handler: a.toolFleetTenantSnapshot,
		},
		{
			Name:        "fleet_tenant_restore",
			Description: "Provider hook used by Backup. Replaces a local tenant's data dir from archive_b64 and restarts it if it was running. Args: tenant_id or scope_id, archive_b64.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tenant_id":   map[string]any{"type": "string"},
					"scope_id":    map[string]any{"type": "string"},
					"scope_kind":  map[string]any{"type": "string"},
					"archive_b64": map[string]any{"type": "string"},
				},
				"required": []string{"archive_b64"},
			},
			Handler: a.toolFleetTenantRestore,
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
