# Jobs (v0.1)

Scheduled-job runner for Apteva. The connective tissue every other
app uses to schedule work without reimplementing a scheduler.

## What's in v0.1

- **Three schedule kinds:** `once` (run at a specific time), `every`
  (interval in seconds), `cron` (5-field expression in a chosen tz).
- **Two target kinds:** `http` (call another app's route, or an
  absolute URL gated by `net.egress`) and `event` (call
  `PlatformAPI.SendEvent` on an instance).
- **6 MCP tools:** `jobs_schedule`, `jobs_cancel`, `jobs_list`,
  `jobs_get`, `jobs_runs`, `jobs_run_now`.
- **REST surface** at `/api/apps/jobs/*` for the dashboard panel and
  for other apps to enqueue without going through MCP.
- **Jobs panel** (vanilla HTML+JS) in `project.page` slot, embeddable
  by other apps with `?owner_app=<slug>` to scope the list.
- **At-least-once delivery** with idempotency keys forwarded to HTTP
  targets, exponential backoff, configurable `max_retries`.
- **Single-replica dispatcher** with a row-level lease so a crashed
  tick doesn't strand a job.
- **Two install scopes**: `project` (one install per Apteva project)
  or `global` (one install across projects, isolated by `project_id`).

## How other apps use it

```bash
# CRM schedules a follow-up email in 3 days.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "follow-up: alice@acme.com",
  "owner_app": "crm",
  "schedule": { "kind": "once", "run_at": "2026-05-02T09:00:00Z" },
  "target": {
    "kind": "http",
    "app":  "crm",
    "path": "/cron/send-followup",
    "body": { "contact_id": 42 }
  },
  "idempotency_key": "followup:42:2026-05-02"
}
```

```bash
# Storage app schedules nightly orphan cleanup.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "nightly cleanup",
  "owner_app": "storage",
  "schedule": { "kind": "cron", "cron": "0 3 * * *" },
  "target":  { "kind": "http", "app": "storage", "path": "/cron/cleanup-orphans" }
}
```

```bash
# Agent (via MCP tool) schedules a reminder back to itself.
{
  "name": "weekly review reminder",
  "schedule": { "kind": "cron", "cron": "0 9 * * 1" },
  "target":   { "kind": "event", "instance_id": 7, "message": "Monday 9am — weekly review" }
}
```

## What's deliberately deferred

- Direct MCP-tool dispatch (target kind `mcp_tool`) — needs a stable
  handle to a running core instance and the tool's auth scope. v0.2.
- Distributed multi-replica dispatcher — the lease column is in place
  so adding it is purely a runtime concern.
- Webhooks app for arbitrary external URLs — `net.egress` lets that
  through today, but a dedicated app (with retries, signing,
  per-domain limits) is a cleaner home.

## Local development

```bash
cd apps/mcp/jobs
go build .
APTEVA_PROJECT_ID=test ./jobs        # binds to :8080
curl http://localhost:8080/health
```

See `migrations/001_init.sql` for the full schema and `main.go`'s
`MCPTools()` for the tool surface.
