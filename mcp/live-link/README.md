# Live Link (v0.2)

Public HTTPS URL for a locally-installed Apteva instance. Two modes:

- **Quick** (default): Cloudflare Quick Tunnel. Anonymous, free,
  fresh `*.trycloudflare.com` URL on every start. Zero config.
- **Named**: a stable URL on a Cloudflare zone you own
  (`https://tunnel.example.com`). Requires a CF API token + account
  ID + zone ID. Restarts reuse the same URL.

## What's in v0.2

- **Mode switch** via the `mode` config field (`quick` | `named`).
- **Quick mode** (unchanged from v0.1): spawns
  `cloudflared tunnel --url <target>` and parses the assigned
  `*.trycloudflare.com` URL out of stderr.
- **Named mode**: on first start the app calls Cloudflare's API to
  - create a `cfd_tunnel` (or adopt an existing one with the same name)
  - PUT its ingress to point `hostname → target_url`
  - upsert a proxied CNAME at `hostname → <tunnel_id>.cfargotunnel.com`
  Connector is run with `tunnel run --token <token>`; URL is known
  up-front, so the runs row gets populated immediately. State (tunnel
  UUID, connector token, DNS record id) lives in `named_tunnels` so
  restarts skip the API roundtrip.
- **One UI toggle** at `project.page` slot: status pill, the live URL
  with a Copy button, a Stop button, and a run history. Status now
  surfaces the configured mode.
- **4 MCP tools**:
  - `expose_start` — idempotent; if a tunnel is already up, returns it.
    Blocks up to 15s for the URL to be assigned before returning.
  - `expose_stop` — sends SIGTERM, falls back to SIGKILL after 5s.
    Named tunnels persist on Cloudflare; only the local connector stops.
  - `expose_status` — current state, last error, mode.
  - `expose_destroy` — named-mode only: delete CNAME + tunnel on CF
    and drop the local row. Refuses while running.
- **Run history** in the app's own SQLite (`runs` table) now tagged
  with the mode it ran in.
- **Crash-safe**: on sidecar boot, any leftover `running` rows from a
  previous process life are marked `orphaned` so the UI doesn't lie.

## What's deliberately deferred

| Capability                                | When  |
|-------------------------------------------|-------|
| Auto-rewrite of `PUBLIC_URL`              | v0.3 — needs a new `POST /api/platform/config` endpoint on apteva-server |
| Pull CF creds from the `cloudflare` integration | v0.3 — once the SDK exposes integration credential reads; for now use the `cf_*` config fields |
| ngrok provider                            | v0.3 — adds a new `ngrok` integration to the catalog |
| Tailscale Funnel provider                 | v0.4 |
| Edge HTTP basic auth                      | v0.3 — cloudflared supports it natively |
| Auto-restart on tunnel drop with backoff  | v0.3 |

## Why this is cleaner for Apteva than for WordPress

WordPress bakes absolute URLs into the database (`siteurl`, `home`,
serialized URLs in post content), which is why no one ships a pure WP
plugin for this — exposing a local WP via tunnel breaks every link
unless you also rewrite the DB. Apteva reads `PUBLIC_URL` at runtime,
so v0.2's auto-rewire is just one config write away.

## Permissions

This app installs as `scope: global` and the operator must be admin.

Declared permissions:

- `db.write.app` — for the `runs` and `named_tunnels` tables
- `net.egress` — cloudflared dials Cloudflare's edge; the app calls
  `api.cloudflare.com` directly in named mode

No `platform.config.write` yet — that permission ships alongside the
v0.3 PUBLIC_URL auto-rewrite work.

## Prerequisites

None for `linux/{amd64,arm64,arm,386}` and `darwin/{amd64,arm64}` — the
first time you click "Go live", the app downloads the matching
cloudflared release from github.com/cloudflare/cloudflared (~30MB,
one-time) and caches it under the install's data dir. Subsequent
starts use the cached copy.

If you already manage cloudflared yourself (`brew install cloudflared`,
`apt install`, custom build, …), the app picks it up from `$PATH`
automatically. To pin a specific binary, set the `cloudflared_path`
config field.

To force a fresh download (e.g. after Cloudflare ships a fix), POST
`/install` or use the "Reinstall binary" link in the UI.

Hosts on unsupported os/arch combos (Windows, FreeBSD, riscv64, …)
get a clean error and must install cloudflared manually + set
`cloudflared_path`. There's no fallback download for those platforms
because Cloudflare doesn't publish releases for them.

## Local development

```bash
cd apps/mcp/live-link
go build .
APTEVA_GATEWAY_URL=http://localhost:5280 \
APTEVA_APP_TOKEN=dev-1 \
APTEVA_PROJECT_ID=test \
DB_PATH=/tmp/live-link.db \
./live-link
```

Then:

```bash
# Start a tunnel (forwards to APTEVA_GATEWAY_URL by default)
curl -X POST http://localhost:8080/start

# Check status — public_url populates within a few seconds
curl http://localhost:8080/status

# Copy the URL out of the response, hit it, see your apteva-server.

# Stop
curl -X POST http://localhost:8080/stop

# Recent runs
curl http://localhost:8080/runs
```

## Architecture

```
┌──────────────┐  POST /start   ┌──────────────────────┐
│  dashboard   │ ─────────────► │  live-link app       │
│  (toggle)    │                │   spawns cloudflared │
└──────────────┘                │   parses public URL  │
                                └──────┬───────────────┘
                                       │
                                       ▼
                               https://<random>.trycloudflare.com
                                       │
                                       ▼
                               Cloudflare edge → apteva-server
```

A single in-process `Manager` (`tunnel.go`) holds the `*exec.Cmd`,
captures stderr to extract the URL, and notifies `main.go` via
callbacks for DB persistence + event emit. There is at most one tunnel
per install in v0.1; concurrent `expose_start` calls return the
existing run rather than spawning a second process.

## Restart semantics

The cloudflared subprocess is the source of truth — if the sidecar
dies, the tunnel dies with it. There is no daemon mode and no
PID-file recovery. On sidecar restart, the run's row is reconciled to
`status='orphaned', exit_reason='sidecar restarted'` so history is
honest about what happened.

The operator (or an agent via `expose_start`) re-establishes the
tunnel on the next click. Quick Tunnels mint a *new* random URL each
time — that's a feature, not a bug, but it's why the v0.2 named-tunnel
feature exists.
