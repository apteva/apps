# Fleet

Control plane for Apteva tenants. Each managed tenant is a separate `apteva` process with its own data directory and port, either on the parent host or on an Instances-managed host. Cross-app features are optional.

## Current behavior

**Fleet v0.10.6** includes the safety and performance fixes from the v0.10.5 audit and uses **app-sdk v0.74.1**. See [AUDIT_FIXES.md](AUDIT_FIXES.md) for the changes, validation and rollout requirements.

- `tenant_create` provisions a local or hosted process, registers its administrator and returns credentials. Generated credentials and setup progress are encrypted and persisted before registration so **Resume setup** can recover a partial failure.
- Setup readiness, clone quarantine, health failure streaks and lifecycle operations are durable state, independent of process status and audit-log retention. Interrupted operations block activation until recovery fences their recorded runtimes.
- Local and hosted tenants receive a minimal environment, tenant-scoped delegated DNS credentials and disjoint application port ranges. Existing listeners are not accepted as managed tenants without identity validation.
- Health polling uses eight workers. Startup reconciliation runs after mounting so an unavailable VPS cannot hide the control plane.
- Snapshots stream by default. Local snapshot staging uses filesystem cloning where available and consistent SQLite copies; compression happens after the tenant restarts. Restores retain the prior directory and restore matching credentials/runtime metadata.
- The panel provides paginated search, operation recovery, template project selection and local storage cleanup. Child logs rotate on the one-minute maintenance cadence, retaining five 10 MiB tails. Copy/truncate preserves descriptors held by children that outlive Fleet; a small concurrent-write window and temporary growth between ticks remain possible.
- `tenant_connect`, tenant control, support login, DNS/ingress, templates and optional A2A remain available. Remote monitoring can be suspended and resumed separately from managed process lifecycle.

## Verification

```sh
env GOWORK=off go test -race -tags=integration ./...
env GOWORK=off go vet ./...
cd ui
bun install --frozen-lockfile
bun run typecheck
bun test fleet-state.test.ts
bun run test:browser
```

Browser tests use installed Chrome on macOS and Playwright Chromium elsewhere (`bunx playwright install chromium`). Tests mock provider APIs; they do not change live tenants or DNS. Linux tests additionally execute hosted installation scripts and concurrency checks. Rebuild the shipped panel from the apps root with `bun run scripts/build-panels.ts --app fleet`.

## Optional A2A pairing (v0.9)

Bind Fleet's optional `a2a >= 0.4.0` dependency to the parent instance's global A2A install to enable automatic pairing. Without that binding Fleet behaves exactly as before.

For each Fleet-managed tenant, Fleet then:

- installs A2A globally in the tenant if it is missing and upgrades older installs;
- seeds the parent as a tenant peer and registers the tenant as a parent peer with one tenant-scoped relationship token;
- makes the tenant A2A install a default for new agents and attaches it to existing agents;
- reconciles immediately during create, key attachment, start, and migration, once on Fleet mount, and every ten minutes afterward;
- removes the parent peer when the tenant is deleted; and
- resets copied A2A private data during clone so the clone gets a new node identity.

`tenant_create`, `tenant_attach_key`, `tenant_start`, and `tenant_migrate` return an `a2a` status object. `tenant_get` exposes the latest pairing result from the audit log. Pairing errors are reported and retried without failing an otherwise healthy tenant.

The allowlists are generic A2A agent selectors, not Fleet-specific exposure tiers:

- `a2a_main_agents_json` controls parent agents visible to tenants.
- `a2a_tenant_agents_json` controls tenant agents visible to the parent.

Both default to `["*"]`. Parent agents still have to be deliberately attached to the parent A2A install. Fleet does not modify instances registered with `tenant_connect`, because it does not own their lifecycle.

Tenant environments use an allowlist and a dedicated DNS capability endpoint; they do not receive Fleet's install token or master key.

## Project template provisioning (v0.10.0)

Fleet can read built-in and project-owned setup templates from the parent
project and apply an exact snapshot to a managed tenant. The Fleet install must
approve `platform.templates.read`; existing installations need their approved
permission snapshot refreshed after upgrading.

- `tenant_template_list` lists the templates visible to the current parent
  project, including built-ins by default.
- `tenant_create` accepts `template_id` and an optional
  `project_description`. After tenant auto-setup, Fleet imports the template
  into the tenant's default project and calls the tenant setup/apply API.
- `tenant_apply_template` applies a parent template to an existing active
  tenant. `target_project_id` disambiguates tenants with multiple projects.
- If auto-setup falls back to `setup_pending`, Fleet stores the requested
  template snapshot and resumes application after `tenant_attach_key`.

Template application is intentionally non-destructive. The tenant preserves
existing agents with matching names, merges dashboard widgets, installs
missing apps when the tenant API key belongs to an admin, and returns any
partial-application warnings in the tool result and Fleet event timeline.

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
