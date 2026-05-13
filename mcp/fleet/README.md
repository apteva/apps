# Fleet

Control plane for a fleet of local apteva tenants. Each tenant is a separate `apteva` process the parent host runs as a supervised child, with its own data dir (`~/.apteva-fleet/<slug>/`) and port. Zero cross-app deps.

## Status — v0.2 (admin-driven bootstrap)

v0.2 changes the auth model: fleet no longer tries to auto-register a user inside the spawned tenant. Instead it mints a setup token, injects it via `APTEVA_SETUP_TOKEN`, and surfaces token + URL back to the operator. The operator finishes admin registration in the browser, then hands fleet the resulting api_key. Three reasons for the change:

- The old v0.1 path read `api_key` from the freshly-created `apteva.json`, but the apteva CLI's `--no-browser` mode deliberately doesn't bootstrap a user — so the key was always empty and `tenant_create` always errored.
- Fleet never has to handle a password. The operator picks credentials directly on the tenant's own dashboard.
- The tenant's setup mode is the existing apteva-server mechanism — no CLI or server changes needed.

What works today:

- `tenant_create` — spawns a fresh apteva tenant in setup-pending mode. Allocates port, makes data dir, runs the apteva CLI with `--data-dir <dir> --port <N> --no-browser` + `APTEVA_SETUP_TOKEN`/`APTEVA_REGISTRATION=setup`, waits for `/api/health`. Returns `{ tenant_id, base_url, status: setup_pending, setup_url, setup_token, next_steps }`.
- `tenant_attach_key` — operator pastes the api_key generated on the tenant dashboard. Fleet validates against `/api/auth/status`, encrypts and stores it, clears the setup token, flips status to `active`.
- `tenant_connect` — registers a pre-existing apteva-server (local or remote) without spawning anything. Skips setup entirely.
- `tenant_list` / `tenant_get` — registry queries, filterable by status/owner/version/kind. `tenant_get` returns the decrypted setup token + URL while status is `setup_pending` so refreshes don't lose the info.
- `tenant_start` / `tenant_stop` — process lifecycle for local tenants. `tenant_start` preserves `setup_pending` status if the tenant was mid-bootstrap.
- `tenant_delete` — stops the process and removes the registry row. For local tenants, wipes the data dir only when `confirm=true`.
- `tenant_support_login` — POSTs to tenant's `/api/admin/support_session`, returns a short-lived URL. **Server route not yet implemented** — falls through with a friendly 404 message.
- `tenant_run_remote` — proxies any tenant-side MCP tool call, JSON-RPC envelope unwrapped. Refuses if tenant is still `setup_pending`.
- **health poller** — every 60s probes active tenants; flips to `disconnected` after 5 consecutive failures. Skips tenants in `setup_pending` / `starting` / `stopped` / `suspended` / `failed`.
- **boot reconciler** — on parent restart, probes each local tenant's port and reattaches by URL (children survive fleet restart because they're spawned in their own process group).
- **UI panel** — full tenant list + detail view + setup-pending banner with copy-token / open-URL / attach-key form. Lives at `ui/FleetPanel.tsx`, slot `project.page`.

## Quick start

```sh
# 1. Make sure `apteva` is on $PATH (or set FLEET_APTEVA_BIN).
which apteva

# 2. Install fleet on your parent apteva instance. Then call:
tenant_create  { "slug": "acme", "owner_email": "ops@acme.com" }

# → {
#     "tenant_id":   "tnt_…",
#     "base_url":    "http://localhost:53217",
#     "status":      "setup_pending",
#     "setup_url":   "http://localhost:53217/?setup=1",
#     "setup_token": "apt_a1b2…",
#     "next_steps":  "..."
#   }

# 3. Open setup_url in a browser. Register an admin email + password,
#    pasting the setup_token when asked. The tenant's setup mode locks
#    after this first registration.

# 4. In the tenant dashboard → API Keys → "New key". Copy the sk-… key.

# 5. Hand it back to fleet:
tenant_attach_key  { "tenant_id": "tnt_…", "api_key": "sk-…" }

# → { "tenant_id": "tnt_…", "status": "active" }
```

In the **Fleet** project page panel, all of the above is one screen — the create dialog returns you to the detail view, which shows the setup banner until you paste the api_key.

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

- **`fleet_tenants`** — id, slug, kind (`local`/`remote`), base_url, config_dir (local only), encrypted api_key, encrypted setup_token (cleared once attached), owner, current/target version, status, last_seen, last_health.
- **`fleet_events`** — append-only audit timeline (spawn_start, spawned, spawn_failed, started, stopped, status_changed, key_attached, support_login, health_failed, remote_call). FK-cascaded on tenant delete.

Statuses: `starting | setup_pending | active | suspended | stopped | disconnected | failed | deleted`.

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
