# Jobs

Scheduled-job runner for Apteva. The connective tissue every other
app uses to schedule work without reimplementing a scheduler.

## Capabilities

- **Four schedule kinds:** `once` (run at a specific time), `every`
  (interval in seconds), `cron` (5-field expression in a chosen tz), and
  `random` (a deterministic number of runs in a local daily window).
- **Three target kinds:** `app_tool` (platform-authorized sibling app
  call), `http` (absolute external URL gated by `net.egress`), and
  `event` (`PlatformAPI.SendEvent` to an agent).
- **7 MCP tools:** `jobs_schedule`, `jobs_cancel`, `jobs_list`,
  `jobs_get`, `jobs_runs`, `jobs_run_now`, `jobs_preview`.
- **REST surface** at `/api/apps/jobs/*` for the dashboard panel and
  for other apps to enqueue without going through MCP.
- **Native React Jobs panel** in the `project.page` slot.
- **At-least-once delivery** with idempotency keys forwarded to HTTP
  targets, exponential backoff, configurable `max_retries`, and stable
  logical occurrence metadata across retries.
- **Configurable HTTP dispatch deadlines**: default `180s` via
  `http_dispatch_timeout_seconds`, with per-job `target.timeout_seconds`
  or `target.timeout_ms` overrides, capped at 300 seconds.
- **Bounded concurrent dispatcher** with uniquely owned, reclaimable
  row leases for safe crash recovery and multi-replica execution.
- **Run retention** controlled by `history_retention_days`.
- **Two install scopes**: `project` (one install per Apteva project)
  or `global` (one install across projects, isolated by `project_id`).

## How other apps use it

```bash
# Run five times at deterministic random times in each Paris local day.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "randomized extraction",
  "timezone": "Europe/Paris",
  "schedule": {
    "kind": "random",
    "period": "day",
    "runs_per_period": 5,
    "window_start": "08:00",
    "window_end": "22:00",
    "min_spacing_minutes": 60
  },
  "target": {
    "kind": "app_tool",
    "app": "web",
    "tool": "web_extractor_run",
    "input": { "extractor_id": 42, "schedule_key": "daily-products" }
  },
  "idempotency_key": "daily-products"
}
```

```bash
# CRM schedules a follow-up email in 3 days.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "follow-up: alice@acme.com",
  "owner_app": "crm",
  "schedule": { "kind": "once", "run_at": "2026-05-02T09:00:00Z" },
  "target": {
    "kind": "app_tool",
    "app":  "crm",
    "tool": "crm_send_followup",
    "input": { "contact_id": 42 }
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
  "target":  { "kind": "app_tool", "app": "storage", "tool": "files_cleanup_orphans", "input": {} }
}
```

```bash
# Invoke a Function through the platform app-call broker.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "sync local google data",
  "owner_app": "flexylead",
  "schedule": { "kind": "cron", "cron": "0 * * * *" },
  "target": {
    "kind": "app_tool",
    "app": "functions",
    "tool": "functions_invoke",
    "input": {
      "name": "flexylead-sync-local-google",
      "event": {}
    }
  }
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

## Local development

```bash
cd apps/mcp/jobs
go build .
APTEVA_PROJECT_ID=test ./jobs        # binds to :8080
curl http://localhost:8080/health
```

See `migrations/` for the schema and `main.go`'s
`MCPTools()` for the tool surface.
