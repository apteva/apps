// Apteva Instances v0.1.0 — compute-host inventory + lifecycle.
//
// Provisions and manages the machines that workloads run on. The
// local Apteva machine is always available as a built-in instance
// (id 0); remote machines come from the bound VPS provider.
//
// The MCP surface is uniform across local and remote: instance_create,
// instance_destroy, instance_run_command, instance_upload_file,
// instance_metrics — same shape, the implementation switches on
// provider='local' vs the SSH-based remote path.
//
// This is the foundation layer for several future apps (Live Link's
// self-vps tunnel, Deploy's SSHRuntime, Backup off-host targets,
// Containers, Database). Each consumer binds Instances as a
// kind=app integration and calls these tools instead of binding a
// VPS provider directly. Single source of truth for the host fleet.
//
// Naming: "instance" here = compute machine (AWS-style). Apteva-
// core's existing "instance" concept (a thinking loop per project)
// is a separate, internal-only model. Same word, different scope —
// the renames in apteva-server side will eventually rename core's
// concept to "agent" and remove the collision.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: instances
display_name: Instances
version: 0.4.4
description: |
  Compute-host inventory for Apteva. Manages local machine + VPS
  instances (Hetzner in v0.1; DO/Vultr/AWS in later releases).
  Foundation layer consumed by Live Link, Deploy, Backup, Containers
  via cross-app calls.
author: Apteva
scopes: [global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
  integrations:
    - role: provider
      kind: integration
      required: false
      compatible_slugs: [hetzner]
      label: VPS provider
      hint: |
        Optional — local instance always available. Bind a VPS integration
        (Hetzner Cloud) to provision remote instances. Future v0.2 adds
        DigitalOcean / Vultr / AWS EC2 to compatible_slugs.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: instance_create,       description: "Provision a new instance via the bound VPS provider. Args: name, provider?, region?, size?, image?, tags?." }
    - { name: instance_get,          description: "Fetch one instance by id." }
    - { name: instance_list,         description: "List instances. Args: provider? (filter), status? (filter)." }
    - { name: instance_destroy,      description: "Terminate the instance (refused for local id 0). Args: id." }
    - { name: instance_run_command,  description: "Execute a shell command. Local: exec; remote: SSH. Args: id, cmd, timeout_s?." }
    - { name: instance_upload_file,  description: "Write a file. Local: filesystem (path-allowlisted); remote: SCP. Args: id, path, content_b64." }
    - { name: instance_wait_ready,   description: "Poll the instance until SSH is reachable. Args: id, timeout_s?." }
    - { name: instance_metrics,      description: "CPU / memory / disk / network / load / uptime. Args: id." }
    - { name: instance_list_server_types, description: "Live list of VPS server types (sizes) from the bound provider — name, cores, memory_gb, disk_gb, price, deprecation. Use to discover valid sizes for instance_create. Args: provider? (default 'hetzner')." }
    - { name: instance_list_locations,    description: "Live list of VPS regions from the bound provider — name, city, country, network_zone. Args: provider? (default 'hetzner')." }
    - { name: instance_list_images,       description: "Live list of bootable OS images from the bound provider (system images only). Args: provider? (default 'hetzner')." }
  ui_panels:
    - { slot: project.page, label: "Instances", icon: server, entry: /ui/InstancesPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/instances
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/instances.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

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
		return errors.New("instances requires a db block")
	}
	globalCtx = ctx

	// Seed the local instance (id=0) on first boot. Idempotent —
	// uses INSERT OR IGNORE so re-running OnMount on every restart
	// is safe. localhost is always 'ready' from the moment the app
	// mounts; nothing to provision.
	if err := ensureLocalInstance(ctx.AppDB()); err != nil {
		return fmt.Errorf("seed local instance: %w", err)
	}
	ctx.Logger().Info("instances mounted",
		"data_dir", ctx.DataDir())

	// Recover any rows left in 'provisioning' by a previous sidecar
	// instance. Two states get handled: rows that may have leaked a
	// VPS (provider_id missing) get a server_list reconciliation
	// against Hetzner; rows whose readiness-probe goroutine
	// evaporated mid-boot get a fresh probe. Best-effort — runs in
	// the background so it doesn't slow OnMount on a slow API call.
	go reconcileHetznerProvisioning(ctx)

	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/instances", Handler: a.handleInstancesCollection},
		{Pattern: "/api/instances/", Handler: a.handleInstanceItem},
		// Live provider catalog. Sister surface to the MCP tools so the
		// panel doesn't need an MCP client; ?provider= defaults to
		// hetzner. Returns the same shape as the MCP tools wrap.
		{Pattern: "/api/instances-server-types", Handler: a.handleListServerTypes},
		{Pattern: "/api/instances-locations", Handler: a.handleListLocations},
		{Pattern: "/api/instances-images", Handler: a.handleListImages},
	}
}

// ─── HTTP helpers (shared) ─────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}

func parseID(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
