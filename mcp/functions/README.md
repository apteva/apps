# Functions 1.8.1

Lambda-style serverless functions for Apteva. Each function is an
immutable, built **version** served by a pool of **warm worker
processes**: the runtime boots once, loads your handler, and then
serves invocations over a socketpair — no per-request process spawn,
cold starts only when no suitable worker is available.

## Upgrading to 1.8.1

Requires Apteva **0.50.2 or newer**. This patch embeds app-sdk v0.76.0 and declares a **1,800-second startup budget**. Health stays HTTP 503 with initialization progress until schema recovery and mounting finish; it becomes 200 only when the app is ready. Initialization cancellation rolls back the active migration transaction.

Migration 005 from 1.8.0 could leave committed columns without its completion receipt after a timeout. Version 1.8.1 recovers every committed prefix of that migration automatically: it checks existing column definitions, preserves existing function and artifact identities, fills only missing identities, creates and validates the invocation index, and commits the repair with the original receipt. A completed 005 is skipped. No invocation history is deleted to speed up the upgrade.

For a failed 1.7.0 → 1.8.0 upgrade, take a consistent database backup and upgrade directly to 1.8.1. Do not manually add the 005 receipt or regenerate existing identities. Index creation scans invocation history and needs free space for its index, SQLite temporary files and transaction journal; startup time depends on data and storage performance. A binary fallback does not reverse previously committed schema changes.

The SQL runner now handles migrations 001–004; `migration.go` applies 005 inside `OnMount`, before any app route is available. Keep future migrations that depend on 005 in that ordered schema-aware runner. The original 1.8.0 SQL is retained under `testdata/` for interruption regression tests, not startup execution.

Run `GOWORK=off go test -run TestExecutionMigration -race .` for every interrupted statement prefix, cancellation rollback and schema conflict tests. `scripts/test-migration-upgrade.py` boots actual app binaries, recreates the reported partial state, kills the app during index creation, verifies rollback, holds a real SQLite writer past the old timeout, retries, checks integrity/identities/receipt, and creates and invokes a function. See its command-line help for a 9.42 GB synthetic fixture.

## Upgrading from 1.7.0

This release improves execution and callback performance and fixes deployment, concurrency, isolation, streaming and panel issues. See [the audit and performance report](AUDIT_FIXES.md) for measurements and validation.

Linux installations must provide delegated cgroup v2 and Landlock ABI 6 (Linux 6.12+). Configure storage quotas or bounded volumes for artifacts and temporary files. Unary/protocol frames are limited to 8 MiB; read APIs mask secrets and paginate results, and SSE clients must check the terminal event. Review the execution and read-API sections below before upgrading.

## The handler contract

**node** — a module that default-exports a handler:

```js
export default async function handler(event, context) {
  // event: the JSON payload (HTTP body, cron, or functions_invoke arg)
  // return any JSON — that's the response
  return { hello: event?.name ?? "world" };
}
```

**go** — `package main` with a `Handle` func and **no `main()`**. The
harness supplies `main()` and the `Context` type; `go build` compiles
them together at deploy:

```go
package main

import "encoding/json"

func Handle(event json.RawMessage, ctx *Context) (any, error) {
  return map[string]any{"hello": "world"}, nil
}
```

Either way, `context` / `ctx` gives you:

- **`context.call(app, tool, input)`** (`ctx.Call` in Go) — invoke
  another Apteva app's MCP tool. The sidecar mediates it; your code
  never holds a platform token.
  `await context.call("tables", "rows_insert", {...})`.
- **`context.integration(conn, tool, input)`** (`ctx.Integration` in
  Go) — invoke a tool on an integration connection (Pushover, Slack,
  Resend, anything in the integrations catalog). `conn` is either a
  numeric connection id or an app slug ("pushover") — the sidecar
  resolves slugs to the single matching connection in this project.
  `await context.integration("pushover", "pushover_send_notification",
  { message: "hi" })`. Returns the upstream tool's response data;
  errors when the upstream returns non-success or the slug doesn't
  match exactly one connection (multi-match returns the candidate
  ids — pass the explicit id to disambiguate).
- **`context.env`** — a *scrubbed* environment: your function's own
  `env` map plus a small host allowlist (`PATH`, locale and TLS
  certificate settings). `HOME` and temporary directories are private. The
  sidecar's secrets (`APTEVA_APP_TOKEN`, gateway URL) are **not** here.
- **`context.log(...)`**, **`context.functionName/functionId/runtime`**.
- **`context.stream` / `context.sse`** — incrementally write HTTP response
  chunks or Server-Sent Events. Go handlers use `ctx.StreamStart`,
  `ctx.StreamWrite`, `ctx.SSE`, and `ctx.SSEComment`.

Top-level / package-level code runs once per worker (cold start) —
put client setup there; it's reused across warm invocations.

## Streaming HTTP responses

Streaming is available on `/fn/<name>` and Function URL HTTP invocations.
The first chunk is flushed immediately through API Gateway. The
`functions_invoke` MCP tool remains unary and returns a capped preview when a
streaming handler is invoked manually or by a job.

Node SSE example:

```js
export default async function handler(event, context) {
  await context.sse.send({ phase: "started" }, { event: "progress" });
  await context.sse.send({ phase: "complete" }, { event: "progress" });
}
```

Node handlers can stream arbitrary bytes with
`await context.stream.start({statusCode, headers})` followed by
`await context.stream.write(chunk)`.

Go SSE example:

```go
func Handle(event json.RawMessage, ctx *Context) (any, error) {
  if err := ctx.SSE("progress", map[string]any{"phase": "started"}); err != nil {
    return nil, err
  }
  if err := ctx.SSE("progress", map[string]any{"phase": "complete"}); err != nil {
    return nil, err
  }
  return nil, nil
}
```

Go handlers can stream arbitrary bytes with `ctx.StreamStart` and
`ctx.StreamWrite`. Handler completion closes the response stream.

## Lifecycle: deploy ≠ invoke

- **Deploy** (`functions_create` for v1, `functions_deploy` after) —
  creates an immutable version, runs `npm install` once if the
  version ships a `package_json`, then validates a sandboxed boot before activation. A lockfile and resolved source snapshot are persisted. Rebuilds use `npm ci`. Rollbacks also validate before activation. A newer deployment or rollback supersedes an older build still in progress.
- **Invoke** — routes the event to a warm worker for the active
  version (cold-starts one if the pool is empty). A new deploy drains
  the previous version's workers.

## Runtimes

- **node** — interpreted; Node 18+ ships a global `fetch`, so
  functions can make outbound HTTP with no dependency. `package_json`
  deps are `npm install`ed once at deploy.
- **go** — compiled; deploy runs `go build`, and the harness is
  compiled into the worker binary. apteva-server already needs a Go
  toolchain on PATH to build kind:source apps, so the runtime is
  guaranteed present. stdlib only for now — third-party Go modules
  are a planned follow-on.

bun is out (its `node:net` can't adopt the inherited socketpair fd);
python is a planned follow-on.

## Triggers

- **HTTP** — `POST /api/apps/functions/fn/<name>`; the request body is
  the event, the handler's return value is the response.
- **Cron** — pair with the Jobs app: an `http` target at
  `app: "functions", path: "/fn/<name>"`.
- **Manual** — the `functions_invoke` MCP tool, or the panel's invoke
  console.

## MCP tools

`functions_create` (creates + deploys v1), `functions_deploy`,
`functions_rollback`, `functions_versions`, `functions_update`
(metadata only — env, limits, status), `functions_delete`,
`functions_list`, `functions_get`, `functions_invoke`,
`functions_invocations`, `functions_logs`.

## Examples

### Create a function (deploys v1)

```json
// functions_create
{
  "name": "hello-world",
  "runtime": "node",
  "source": "export default async (event) => ({ hello: event?.name ?? 'world' });"
}
```

```bash
curl -X POST https://<host>/api/apps/functions/fn/hello-world \
  -H 'Content-Type: application/json' -d '{"name":"Marco"}'
# → {"hello":"Marco"}
```

### A function with dependencies

```json
// functions_create
{
  "name": "report",
  "runtime": "node",
  "source": "import ky from 'ky';\nexport default async (e) => ({ status: (await ky.get(e.url)).status });",
  "package_json": "{\"dependencies\":{\"ky\":\"^1.0.0\"}}"
}
```

`npm install` runs once at deploy — never on the invoke path.

### Call another app

```js
export default async function handler(event, context) {
  const { ids } = await context.call("tables", "rows_insert", {
    table: "leads", rows: [{ email: event.email }],
  });
  return { inserted: ids[0] };
}
```

More worked examples — simple JSON returns and Tables-app
interaction — live in [`examples/`](./examples).

### Deploy a new version, then roll back

```json
// functions_deploy  →  builds v2, makes it active
{ "name": "hello-world", "source": "export default async () => ({ v: 2 });" }

// functions_rollback  →  active version back to v1
{ "name": "hello-world", "version": 1 }
```

## Isolation and capacity

On Linux, dependency builds and warm workers pass through the binary's
credential-free sandbox helper. It applies Landlock filesystem rules,
`no_new_privs`, a deny-list seccomp filter, process/file limits, private
HOME/tmp directories, and read-only artifacts. Cgroup v2 CPU, process and
memory limits require a delegated writable cgroup v2 root by default. Required isolation needs scoped Landlock signals/abstract sockets (ABI 6, Linux 6.12+) and the native amd64/arm64 syscall ABI. The helper remains on one OS thread through exec. Builds fail closed when these requirements cannot be met.

Useful operator settings:

- `APTEVA_FUNCTIONS_CGROUP_ROOT` — delegated cgroup v2 directory.
- `APTEVA_FUNCTIONS_REQUIRE_CGROUP=true` (Linux default) — fail closed without hard cgroups. Set false only for trusted development inside an independently limited container.
- `APTEVA_FUNCTIONS_REQUIRE_SANDBOX=false` — emergency Linux compatibility
  escape hatch; Linux otherwise fails closed if Landlock/seccomp cannot load.
- `APTEVA_FUNCTIONS_MAX_WORKERS=32`, `APTEVA_FUNCTIONS_MAX_QUEUE=256`,
  `APTEVA_FUNCTIONS_MAX_QUEUE_PER_FUNCTION=64`, and
  `APTEVA_FUNCTIONS_MAX_BUILDS=2` — process-wide backpressure.
- `APTEVA_FUNCTIONS_MAX_DOWNSTREAM_CALLS=16` — concurrent
  `context.call`/`context.integration` fan-out per invocation.
- `APTEVA_FUNCTIONS_INVOCATION_RETENTION_DAYS=30` — audit-log retention.

macOS local development keeps the same Node/Go contracts and resource/env
scrubbing, but does not provide Linux kernel isolation.

## What's deferred

- **python runtime** — needs its own harness; same socketpair
  protocol, so it's additive.
- **third-party Go modules** — go functions are stdlib-only for now;
  a future version takes a user-supplied `go.mod`.

## Local development

```bash
cd apps/mcp/functions
go build .
GOWORK=off go test -count=1 -timeout=20m .  # real Node and Go workers
APTEVA_PROJECT_ID=test ./functions       # binds to :8080
```

Panel source is `ui/FunctionsPanel.tsx`; the worker harness is
`harness/node.mjs` (embedded into the binary). Rebuild the panel with
`bun run scripts/build-panels.ts` from `apps/`.

## Execution limits, cancellation and access

The request deadline covers admission, rebuild, worker boot and execution. Production app/integration callbacks use context-aware HTTP with a shared keep-alive transport. This app-local adapter uses the public SDK callback endpoints because SDK v0.76.0 still lacks context parameters. Custom legacy SDK clients keep a downstream slot until their actual operation returns. Cancellation cannot undo an upstream side effect already accepted; use atomic operations/idempotency for retries.

`access` optionally narrows installation grants: `{"apps":["tables.rows_list"],"integrations":["pushover.*"]}`. An explicit empty array denies that class of call; `null` inherits installation grants. Numeric connection IDs require matching numeric policy rules. Updating environment, memory or access drains workers with stale configuration. Handler background work after completion is unsupported; invocation-scoped logging, calls and streams reject it.

HTTP events are limited to 1 MiB; CRUD bodies to 2 MiB. Protocol frames and complete unary results are limited to 8 MiB, including their JSON envelope. Larger content should use storage references or streaming (64 KiB chunks). Serialization/size errors return explicit errors. Stored response previews are capped at 64 KiB, logs at 16 KiB and event previews at 4 KiB; the caller's unary response is not truncated. Native stdout/stderr is best-effort; use `context.log` / `ctx.Log` for invocation-scoped attribution. Environment secrets are redacted from stored logs and public URL tokens from stored request paths.

SSE streams end with `apteva.complete` or `apteva.error` carrying `{status,error,invocation_id}`. Clients must treat a stream missing its terminal event as incomplete. Arbitrary byte streams use the status trailer and abort the HTTP stream on failure. Headers may already be 200 when a handler fails. The browser console displays chunks incrementally, caps its display, supports cancel, and recognizes the SSE terminal status. Jobs HTTP targets should use unary handlers unless their dispatcher validates the terminal event or trailer; HTTP 200 alone cannot prove streamed execution succeeded.

Invocation rows are created before execution and finalized with actual elapsed time, version ID, configuration digest, truncation and build/queue/cold-start/execution timings. Interrupted builds/invocations become failed/error at restart. Legacy repo versions recover matching source snapshots from old artifacts when available; if both snapshot and artifacts are missing and the repository changed, invocation refuses the drift and requires an explicit redeploy.

Additional operator settings (defaults):

| Setting | Meaning |
|---|---|
| `APTEVA_FUNCTIONS_TOTAL_MEMORY_MB=4096` | Memory reservation budget for all live workers, including idle workers |
| `APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB=128` | In-flight protocol payload reservations; JSON decoding adds overhead, so also limit sidecar container memory |
| `APTEVA_FUNCTIONS_MAX_DOWNSTREAM_TOTAL=64` | Process-wide downstream operations, retained until actual completion |
| `APTEVA_FUNCTIONS_MAX_BUILD_QUEUE=16` | Bounded build admission |
| `APTEVA_FUNCTIONS_TEMP_MB=128` | Per-worker temporary-file budget, checked every 250 ms |
| `APTEVA_FUNCTIONS_BUILD_DISK_MB=1024` | Temporary build budget, checked every 250 ms |
| `APTEVA_FUNCTIONS_ARTIFACT_MB=512` | Maximum published artifact size |
| `APTEVA_FUNCTIONS_TOTAL_ARTIFACT_MB=4096` | Artifact admission budget |
| `APTEVA_FUNCTIONS_KEEP_VERSIONS=20` | Retain at least the newest 20 versions; prune older inactive versions after seven days |
| `APTEVA_FUNCTIONS_STDLIB_CACHE` | Optional trusted standard-library seed directory (default under build base) |

Polling disk usage is not an instantaneous hard quota. Configure filesystem/project quotas or bounded volumes for both `APTEVA_DATA_DIR` and the sidecar temporary directory when running untrusted writers. Cgroups do not enforce disk quotas. `/capabilities` exposes configured isolation requirements and current process/memory/downstream counters; successful worker validation confirms kernel setup for that worker.

Go builds receive independent copies of a trusted standard-library cache compiled with matching flags. Untrusted function builds never populate or modify that shared seed. Build-cache scratch files are removed before publishing immutable artifacts. Harness/build fingerprints invalidate stale artifacts after upgrades.

## Read APIs and performance verification

Function lists omit source, environment values and URL tokens. Function detail masks secrets unless `include_secrets=true` (MCP) or `include_secrets=1` (HTTP) is explicitly requested. Function/version/invocation lists return `next_cursor`; pass it as `cursor` for the next page. Invocation lists are summaries; `/invocations/<id>` and `functions_logs` return stored previews. The panel fetches independent details concurrently and rejects stale selection responses.

Run the opt-in workload with `GOWORK=off GOMAXPROCS=4 FUNCTIONS_PERFORMANCE=1 go test -run '^TestPerformance$' -count=1 -v`. It measures real local HTTP invocation and app callbacks with fixed 2 ms downstream latency, first-use/deploy time, p50/p95, allocation bytes and failed requests. `BenchmarkEventRead` isolates HTTP event parsing. Compare identical workloads with release `functions/v1.7.0`; retain error counts and interleave runs on a shared host. Faster first invocation partly reflects boot validation performed during deployment, not elimination of boot cost.
