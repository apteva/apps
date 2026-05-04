# Backup (v0.1)

Periodic backups of your Apteva instance — server DB plus every
installed app's data — driven by the platform snapshot endpoint and
shipped to a destination of your choice.

## What's in v0.1

- **Two destinations**: `local` (host directory) and `s3` (any
  S3-compatible bucket — AWS S3, Cloudflare R2, Backblaze B2, Wasabi,
  MinIO).
- **Cron policies** registered with the [`jobs`](../jobs/) app — one
  scheduler used across the platform, one place to view "what's
  running when?".
- **Retention pruning** — `keep_last_n` per policy, with `apteva-`
  prefix matching so files the operator dropped in the same bucket
  survive.
- **Restore from history** — UI button (or `backup_restore` MCP tool)
  pulls the bytes back from the destination and POSTs them to
  `/api/platform/restore`. App DBs swap live; the platform DB stages
  for the next server boot.
- **3 MCP tools** for agent-driven operation:
  - `backup_now`
  - `backup_list`
  - `backup_restore`
- **One UI panel** at `project.page` slot — status, destinations,
  policies, history.

## What's deliberately deferred

| Capability                | When                                    |
|---------------------------|-----------------------------------------|
| Encryption (`age` passphrase) | v0.2 — config field exists, not wired |
| `storage_app` destination     | v0.2 — manifest declares optional dep |
| Incremental backups (chunks) | Maybe v0.3, maybe never (full snapshots compress well) |
| Verify (round-trip a backup)  | v0.2 |
| Multi-recipient encryption (host A backs up, host B restores) | v0.3 |

## Architecture

```
┌──────────────────────┐  cron tick   ┌──────────────────────┐
│  jobs app            │ ───────────► │  backup app          │
│                      │   POST /run  │                      │
└──────────────────────┘              └──────┬───────────────┘
                                              │
                                              │  GET /api/platform/snapshot
                                              ▼
                                       ┌──────────────────────┐
                                       │  apteva-server       │
                                       │   • VACUUM INTO each │
                                       │     SQLite DB        │
                                       │   • streams tar.gz   │
                                       └──────┬───────────────┘
                                              │
                                              ▼
                                       ┌──────────────────────┐
                                       │  destination         │
                                       │  local | s3 | r2     │
                                       └──────────────────────┘
```

The platform owns the privileged primitive (read every install's data
dir + the server DB). This app owns scheduling, destinations,
retention, and the UI. That split lets a third-party ecosystem of
backup apps exist without server changes — `backup-borg`,
`backup-restic`, `backup-tarsnap` are all viable plugins on top of the
same `/api/platform/snapshot` and `/api/platform/restore` endpoints.

## Permissions

This app installs as `scope: global` and the operator must be admin
(user_id 1) — the snapshot/restore endpoints reject everyone else.

The relevant declared permissions are:

- `db.write.app` — its own bookmarks/policies/run log
- `net.egress` — talk to S3/R2/B2 endpoints
- `platform.apps.call` — call `jobs_schedule` / `jobs_cancel`

## S3 credentials (status)

The S3 destination is wired end-to-end — `minio-go` client, list,
put, get, delete, retention prune — but the credentials adapter
currently returns "incomplete; see README" because the SDK's typed
`PlatformConnection` doesn't yet expose the raw access/secret pair.

Two paths forward, neither blocking v0.1's local destination:

1. **SDK extension**: add `GetConnectionWithSecrets(id)` returning
   the decrypted credential JSON. Tightly scoped — only the install's
   own connections, only when `platform.connections.read` is declared.
2. **Env passthrough**: read `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY`
   from the install's config (each destination gets its own pair).
   Lower-friction for self-hosters, but credentials live in
   `apteva-server.db` config rather than the structured connections
   store.

The local destination works fully today.

## Local development

```bash
cd mcp/backup
go build .
APTEVA_GATEWAY_URL=http://localhost:5280 \
APTEVA_APP_TOKEN=dev-1 \
APTEVA_PROJECT_ID=test \
DB_PATH=/tmp/backup.db \
./backup
```

Then:
```bash
# Create a local destination
curl -X POST http://localhost:8080/destinations \
  -H 'Content-Type: application/json' \
  -d '{"name":"laptop","kind":"local","config":{"path":"/tmp/apteva-backups"}}'

# Run a backup right now
curl -X POST http://localhost:8080/run -d '{}'

# List runs
curl http://localhost:8080/runs
```

See `migrations/001_init.sql` for the full schema and `main.go`'s
`MCPTools()` for the tool surface.

## Restore semantics

App DBs are restored **live**: the supervisor stops the sidecar, the
file is atomically replaced, stale `-wal`/`-shm` companions are
removed, and `ResumeLocalInstalls` re-spawns it.

The platform DB itself can't be replaced under a running server, so
it's **staged**: the new bytes land at `<dbPath>.restored` with a
marker file, and apteva-server's boot path swaps it in on the next
restart. The UI surfaces a clear "restart required" banner; the app
does not try to restart the platform itself.

The previous bytes are kept as `.prerestore-<timestamp>` files at the
same path — manual rollback is `mv <file>.prerestore-<ts> <file>`.
