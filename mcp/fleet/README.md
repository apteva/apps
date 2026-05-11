# Fleet

Control plane for a fleet of local apteva tenants. Each tenant is a separate `apteva` process the parent host runs as a supervised child, with its own data dir (`~/.apteva-fleet/<slug>/`) and port. Zero cross-app deps.

## Status — v0.1 (local processes, single host)

What works today:

- `tenant_create` — spawns a fresh apteva tenant: allocates port, makes data dir, runs the apteva CLI with `--data-dir <dir> --port <N> --no-browser`, waits for `/api/health`, reads `api_key` from the generated `apteva.json`, encrypts it, persists the row.
- `tenant_connect` — registers a pre-existing apteva-server (local or remote) without spawning anything.
- `tenant_list` / `tenant_get` — registry queries, filterable by status/owner/version/kind.
- `tenant_start` / `tenant_stop` — process lifecycle for local tenants (re-spawn at the same port + data dir / SIGTERM → 10s → SIGKILL).
- `tenant_delete` — stops the process and removes the registry row. For local tenants, wipes the data dir only when `confirm=true`.
- `tenant_support_login` — POST to tenant's `/api/admin/support_session`, returns a short-lived URL.
- `tenant_run_remote` — proxy any tenant-side MCP tool call, with JSON-RPC envelope unwrap.
- **health poller** — every 60s probes active tenants; flips to `disconnected` after 5 consecutive failures.
- **boot reconciler** — on parent restart, probes each local tenant's port and reattaches by URL (children survive fleet restart because they're spawned in their own process group).

## Quick start

```sh
# 1. Make sure `apteva` is on $PATH (or set FLEET_APTEVA_BIN).
which apteva

# 2. Install fleet on your parent apteva instance (via the dashboard
#    or whatever app-install flow you use). Then call:
tenant_create  { "slug": "acme", "owner_email": "ops@acme.com" }

# 3. fleet logs:
#   fleet: tenant spawned tenant=tnt_… slug=acme port=53217
#   → http://localhost:53217/  (data dir: ~/.apteva-fleet/acme/)
```

## Honest limits of v0.1

- **No isolation.** All tenants share the parent's OS, disk, kernel, network. A runaway tenant can DoS the others. Fine for trusted clients (you control them); not OK for hostile multi-tenancy.
- **Single host.** No remote / VPS provisioning. ~50 tenants per host is plausible.
- **Localhost only.** Tenants are reachable on `http://localhost:<port>` from the parent's machine — no public domain, no TLS. Use SSH port-forwarding or wait for v0.2.
- **No per-tenant version drift.** Every tenant runs whatever `apteva` binary `FLEET_APTEVA_BIN` (or `$PATH`) resolves to. Per-tenant pinning needs binary-per-tenant or container-per-tenant.

## Configuration

| Env | Default | Purpose |
|---|---|---|
| `FLEET_APTEVA_BIN` | `apteva` on `$PATH` | Path to the apteva CLI binary the supervisor spawns. |
| `FLEET_DATA_ROOT` | `~/.apteva-fleet` | Root directory under which each tenant's data dir lives. |
| `FLEET_MASTER_KEY` | auto-generated `<DataDir>/master.key` | base64-encoded 32-byte key for encrypting tenant api_keys at rest. |

## Schema

Two tables (both prefixed `fleet_`):

- **`fleet_tenants`** — id, slug, kind (`local`/`remote`), base_url, config_dir (local only), encrypted api_key, owner, current/target version, status, last_seen, last_health.
- **`fleet_events`** — append-only audit timeline (spawn_start, spawned, spawn_failed, started, stopped, status_changed, support_login, health_failed, remote_call). FK-cascaded on tenant delete.

Statuses: `starting | active | suspended | stopped | disconnected | failed | deleted`.

## What lives where

- `main.go` — App interface, embedded manifest, tool registration.
- `tenants.go` — DB store + `Tenant` / `Event` types.
- `localproc.go` — spawn / supervise / port-allocation / boot-reconciler.
- `handlers.go` — MCP + HTTP handlers.
- `health.go` — 60s health poller worker.
- `remote.go` — `tenant_run_remote` (proxies through to a tenant's MCP gateway, unwraps the envelope).
- `crypto.go` — AES-GCM keyring for tenant api_keys at rest.
- `migrations/001_init.sql` — schema.

## Future (post-v0.1)

Forward-compatible: the `kind` column already distinguishes `local` from `remote`. To add new provisioners, introduce new `kind` values without breaking existing rows or tools.

- v0.2: `tenant_create` accepts `kind=docker` (parent host's docker daemon) — same registry, just a different spawn backend.
- v0.3: optional dep on `instances` for `kind=vps` — fleet runs `instance_run_command` to launch apteva on a remote host.
- v0.4: optional dep on `domains` + `certs` for public per-tenant URLs.
- v0.5: optional dep on `storage` for `tenant_backup_now` / `tenant_restore`.

## Dev

```sh
cd apps/mcp/fleet
go build ./...
go vet ./...
go test ./...     # tests TBD
```

To boot fleet standalone for hand-testing (per app-sdk dev convention):

```sh
APTEVA_GATEWAY_URL=http://localhost:5280 \
  APTEVA_APP_TOKEN=dev-token \
  APTEVA_INSTALL_ID=0 \
  FLEET_APTEVA_BIN=$(which apteva) \
  go run .
```
