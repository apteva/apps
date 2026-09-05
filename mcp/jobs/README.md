# Jobs

Scheduled-job runner for Apteva. The connective tissue every other
app uses to schedule work without reimplementing a scheduler.

## Capabilities

- **Four schedule kinds:** `once` (run at a specific time), `every`
  (interval in seconds), `cron` (5-field expression in a chosen tz), and
  `random` (a deterministic number of runs in a local daily window).
- **Three target kinds:** `app_tool` (platform-authorized sibling app
  call), `http` (absolute external URL gated by `net.egress`), and
  `event` (a project-validated platform event to an agent).
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
  row leases, renewal, fair project scheduling and cancellation of in-flight requests.
- **Retention**: run history defaults to 30 days; terminal jobs to 90 days. Both are configurable and support 0 to disable.
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
# Schedule a follow-up at an explicit time (replace the example timestamp).
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "follow-up: alice@acme.com",
  "schedule": { "kind": "once", "run_at": "2027-05-02T09:00:00Z" },
  "target": {
    "kind": "app_tool",
    "app":  "crm",
    "tool": "crm_send_followup",
    "input": { "contact_id": 42 }
  },
  "idempotency_key": "followup:42:2027-05-02"
}
```

```bash
# Storage app schedules nightly orphan cleanup.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "nightly cleanup",
  "schedule": { "kind": "cron", "cron": "0 3 * * *" },
  "target":  { "kind": "app_tool", "app": "storage", "tool": "files_cleanup_orphans", "input": {} }
}
```

```bash
# Invoke a Function through the platform app-call broker.
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "sync local google data",
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
  "target":   { "kind": "event", "agent_id": "self", "message": "Monday 9am — weekly review" }
}
```

## Scheduling and delivery contracts

- `once` delivers its absolute timestamp when due. In the panel, the input is explicitly browser-local and the resolved UTC instant is shown before creation.
- `every` advances from completion by the configured interval. `cron` selects the next matching real minute after completion in the chosen IANA timezone. Missed interval/cron occurrences are skipped, not replayed. Nonexistent DST wall times are skipped; repeated matching wall times represent two distinct instants. Leap-day expressions are supported, and impossible expressions are rejected.
- `random` preserves its deterministic logical occurrence sequence. After downtime, missed occurrences are replayed in order; minimum spacing describes scheduled times, not physical delivery spacing during recovery.
- `run-now` queues a separate, one-time child occurrence with its own retry identity and logs it under the original job. It preserves the normal next occurrence. Concurrent duplicate manual requests return 409. Completed/failed jobs can be run manually; cancelled or currently running jobs cannot. Cancelling a job also cancels its queued children.
- Delivery is **at least once**. A process failure after a recipient acts but before Jobs commits can cause a retry. Recipients should deduplicate using the supplied occurrence key. `idempotency_key` identifies delivery attempts; it does not deduplicate job creation. Cancellation cannot undo work already accepted by a recipient.
- The installation has one execution pool (default 8, max 50), shared fairly across stored project partitions including `""`. Capacity is acquired before claims; ten-minute claims renew every 15 seconds. HTTP requests are capped at five minutes and platform calls at ten minutes. Shutdown cancels network I/O and records an interrupted outcome using a bounded finalization context.

## Scope, authorization and private data

Global installations accept authenticated MCP project scope, including an explicitly empty project for global work. MCP `jobs_schedule` preserves the SDK's trusted caller identity; model-supplied project/owner fields cannot replace it. Agent ownership and the current `jobs.schedule` grant are rechecked before deferred delivery. The scheduling grant authorizes use of Jobs' configured target capabilities. Sibling app calls remain authorized by the platform broker. Reserved routing metadata in target input is replaced; event targets must belong to the stored job project.

Direct MCP scheduling without caller headers is supported only on fixed-project installations. Global scheduling requires platform-bound caller headers; administrators can also use the HTTP surface. HTTP ownership fields are ignored. Apps that need ownership attribution should call `jobs_schedule` through the app broker.

For projectless HTTP management, use `/api/apps/jobs/jobs?scope=global&install_id=...`, **omitting `project_id`**. The platform proxy enforces global administrator access. The native panel exposes this scope for global installations. Normal project requests use `project_id`. `/scope` identifies the installation type; `/status` reports dispatcher scan time, capacity, active delivery count and infrastructure failure count.

All API/MCP job results are safe projections: list results contain target kind only; detail results hide payloads, headers, URL paths/query credentials and delivery keys. Raw target data remains available internally for execution. Arbitrary response bodies are neither stored nor returned. Public error messages are categorical so recipients cannot echo credentials into the run log. Clients must retain their input payload if they need to create another schedule.

HTTP targets retain private-network access by default (`allow_private_http=true`) for compatibility with internal webhooks. Set it to `false` for public-address-only dispatch: DNS results are checked and the transport connects to the checked address. Cross-origin redirects are always rejected to prevent forwarding custom credentials. Platform tokens are only used on platform callbacks.

## Limits, pagination and retention

`jobs_list` / `GET /jobs` accept `before` (exclusive job-ID cursor), `search` (name or exact ID), owner and status filters, and `limit` (1–500). `jobs_runs`, `GET /jobs/:id/runs`, and `GET /runs` accept `before` and a 1–200 limit. Pages return `has_more` and, when needed, `next_cursor`. The native and legacy panels expose additional pages.

Requests are limited to 128 KiB; targets to 64 KiB; names to 200 bytes. Each project may have 10,000 pending/running occurrences. Numeric inputs must be integral: intervals 1 second–365 days, retries 0–20, retry base 1–86,400 seconds, random runs 1–100/day, spacing 0–1,440 minutes. Exponential backoff is capped at 24 hours. Responses from targets are limited to 1 MiB. Database operations inherit request cancellation and have a five-second ceiling.

Run history (`history_retention_days`, default 30) and terminal jobs (`terminal_job_retention_days`, default 90) are pruned in bounded batches. Deleting terminal jobs also removes their remaining history; active children protect their parent from deletion. Set either option to `0` to retain that data indefinitely. Job and run IDs remain monotonic after pruning. Pending/running rows are not subject to terminal retention.

Migration 004 normalizes due/lease times to UTC, preserves existing retry identity strings, marks pending rows with no valid next occurrence failed, and adds manual occurrences and indexes. Apply migrations through the SDK; back up the database before a production upgrade. No production upgrade is performed by the test suite.

## Local development

```bash
cd apps/mcp/jobs
GOWORK=off go build .
APTEVA_PROJECT_ID=test ./jobs        # binds to :8080
curl http://localhost:8080/health
```

See `migrations/` for the schema and `main.go`'s
`MCPTools()` for the tool surface.


Requires **Go 1.26.6 or newer** and Bun. The SDK is pinned to `v0.74.1`; standalone verification disables the workspace overlay so it tests the actual release dependency.

```sh
./verify.sh
GOWORK=off go test -run '^$' -bench '^BenchmarkAudit' -benchmem
```

`verify.sh` installs the locked development dependencies, typechecks and rebuilds the minified panel, runs UI, unit, integration and race tests, runs `go vet`, builds a temporary binary, and scans dependencies with govulncheck. See `AUDIT_FIXES.md` for the audit mapping and measured results.
