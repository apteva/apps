// Apteva Instances — compute-host inventory + lifecycle.
//
// Provisions and manages the machines that workloads run on. The
// local Apteva machine is always available as a built-in instance
// (id 0); remote machines come from the bound VPS provider.
//
// The MCP surface is uniform across local and remote: instance_create,
// instance_destroy, instance_run_command, instance_upload_file,
// instance_download_file,
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
version: 0.4.28
description: |
  Compute-host inventory for Apteva. Manages local machine + VPS
  instances through a generic provider binding. Compatible provider
  integrations: Hetzner Cloud, DigitalOcean, Contabo, Vultr, AWS EC2,
  Scaleway (virtual instances and Apple silicon Mac minis), Huawei Cloud,
  Linode, OVHcloud, and RunPod. Foundation layer
  consumed by Live Link, Deploy, Backup, Containers via cross-app
  calls.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.connections.read_public_config
  integrations:
    - role: provider
      kind: integration
      mode: multiple
      required: false
      compatible_slugs: [hetzner, digitalocean, contabo, vultr, aws-ec2, scaleway, huawei-cloud, linode, ovhcloud, runpod]
      label: VPS providers
      hint: |
        Optional — local instance always available. Bind one or more VPS integrations
        to provision remote instances through the generic Instances interface.
        Every compatible provider has catalog, provisioning, readiness, and
        recovery adapters. Immediate destroy is available except on Contabo,
        whose API only schedules contract cancellation. Scaleway macOS hosts
        have a mandatory 24-hour minimum allocation before Destroy is enabled.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: instance_list_providers, description: "List bound VPS provider connections and identify the configured default provider." }
    - { name: instance_create,       description: "Provision a new instance via a bound VPS provider. Args: name, provider? (configured default when omitted), region?, size?, image?, tags?." }
    - { name: instance_get,          description: "Fetch one instance by id." }
    - { name: instance_list,         description: "List instances. Args: provider? (filter), status? (filter)." }
    - { name: instance_destroy,      description: "Terminate the provider-managed instance and remove its row where supported (refused for local id 0 and Contabo). Args: id." }
    - { name: instance_upgrade,      description: "In-place resize of a remote instance where the provider adapter supports it. Hetzner is implemented today. Args: id, size, upgrade_disk?. Always waits for SSH readiness." }
    - { name: instance_run_command,  description: "Execute a shell command. Local: exec; remote: SSH. Args: id, cmd, timeout_s?." }
    - { name: instance_upload_file,  description: "Write a file. Local: filesystem (path-allowlisted); remote: SCP. Args: id, path, content_b64." }
    - { name: instance_download_file, description: "Read a file. Local: filesystem (path-allowlisted); remote: SSH cat. Args: id, path." }
    - { name: instance_open_tunnel,  description: "Open or reuse a loopback-only TCP tunnel through remote SSH. Args: id, target_port." }
    - { name: instance_close_tunnel, description: "Close a loopback TCP tunnel. Args: id, target_port." }
    - { name: instance_wait_ready,   description: "Poll the instance until SSH accepts the key and can run a non-interactive command. Args: id, timeout_s?." }
    - { name: instance_metrics,      description: "CPU / memory / disk / network / load / uptime for local, remote Linux, and remote macOS hosts. Args: id." }
    - { name: instance_list_server_types, description: "Live list of active, non-deprecated compute types from the bound provider — name, cores, memory_gb, disk_gb, platform, resource_class, price, available_in. Includes virtual and bare-metal types where supported. Args: provider? (default: bound provider)." }
    - { name: instance_list_locations,    description: "Live list of VPS regions from the bound provider — name, city, country, network_zone. Args: provider? (default: bound provider)." }
    - { name: instance_list_images,       description: "Live list of bootable OS images from the bound provider, with platform, resource class, location, and server-type compatibility. Args: provider? (default: bound provider)." }
  publishes:
    - name: instance.created
      description: A new instance row was created in Instances.
      payload:
        id: integer
        name: string
        provider: string
        status: string
    - name: instance.provisioning
      description: A remote instance entered provisioning.
      payload:
        id: integer
        name: string
        provider: string
        status: string
        region: string
        size: string
        image: string
    - name: instance.ready
      description: An instance became ready for SSH-backed operations.
      payload:
        id: integer
        name: string
        provider: string
        status: string
        public_ipv4: string
        public_ipv6: string
        ready_at: string
    - name: instance.upgrading
      description: An instance entered an in-place server type upgrade.
      payload:
        id: integer
        name: string
        provider: string
        status: string
        size: string
    - name: instance.upgraded
      description: An instance completed an in-place server type upgrade.
      payload:
        id: integer
        name: string
        provider: string
        status: string
        old_size: string
        new_size: string
        upgrade_disk: boolean
    - name: instance.error
      description: An instance entered an error state.
      payload:
        id: integer
        name: string
        provider: string
        status: string
        error: string
    - name: instance.destroyed
      description: An instance was destroyed and removed from Instances.
      payload:
        id: integer
        name: string
        provider: string
        status: string
        provider_id: string
        destroyed_at: string
  ui_panels:
    - { slot: project.page, label: "Instances", icon: server, entry: /ui/InstancesPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: instances/v0.4.28
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
	// instance. Rows with a recorded provider_id get a fresh readiness
	// probe; rows without provider_id are marked error and left for
	// manual cloud cleanup. We never infer a provider_id by name,
	// because destroy must only target an upstream id recorded from
	// the original create response.
	go reconcileHetznerProvisioning(ctx)
	go reconcileDigitalOceanProvisioning(ctx)
	go reconcileRunPodProvisioning(ctx)
	go reconcileAPIProviderProvisioning(ctx)
	go reconcileHetznerUpgrading(ctx)
	go reconcileDestroying(ctx)

	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	globalTunnelRegistry.closeAll()
	globalSSHPool.closeAll()
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/instances", Handler: a.handleInstancesCollection},
		{Pattern: "/api/instances/", Handler: a.handleInstanceItem},
		{Pattern: "/api/instances-providers", Handler: a.handleListProviders},
		// Live provider catalog. Sister surface to the MCP tools so the
		// panel doesn't need an MCP client; ?provider= defaults to
		// the bound provider. Returns the same shape as the MCP tools wrap.
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
