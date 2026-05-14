# Functions (v1.0)

Lambda-style serverless functions for Apteva. Each function is an
immutable, built **version** served by a pool of **warm worker
processes**: the runtime boots once, imports your handler module, and
then serves invocations over a socketpair — no per-request process
spawn, no per-request cold start.

## The handler contract

A function is a module that default-exports a handler:

```js
export default async function handler(event, context) {
  // event: the JSON payload (HTTP body, cron, or functions_invoke arg)
  // return any JSON — that's the response
  return { hello: event?.name ?? "world" };
}
```

`context` gives you:

- **`context.call(app, tool, input)`** — invoke another Apteva app's
  MCP tool. The sidecar mediates it; your code never holds a platform
  token. `const row = await context.call("tables", "tables_insert_row", {...})`.
- **`context.env`** — a *scrubbed* environment: your function's own
  `env` map plus a small host allowlist (`PATH`, `HOME`, …). The
  sidecar's secrets (`APTEVA_APP_TOKEN`, gateway URL) are **not** here.
- **`context.log(...)`**, **`context.functionName/functionId/runtime`**.

Top-level module code runs once per worker (cold start) — put client
setup there; it's reused across warm invocations.

## Lifecycle: deploy ≠ invoke

- **Deploy** (`functions_create` for v1, `functions_deploy` after) —
  creates an immutable version, runs `npm install` once if the
  version ships a `package_json`, and on a successful build makes it
  the active version. `functions_rollback` repoints the active
  version at an older built one.
- **Invoke** — routes the event to a warm worker for the active
  version (cold-starts one if the pool is empty). A new deploy drains
  the previous version's workers.

## Runtimes

**node** only. bun was a candidate but its `node:net` can't adopt the
inherited socketpair fd the pool uses; python is a planned follow-on.
Node 18+ ships a global `fetch`, so functions can make outbound HTTP
with no dependency.

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
  const row = await context.call("tables", "tables_insert_row", {
    table: "leads", row: { email: event.email },
  });
  return { inserted: row.id };
}
```

### Deploy a new version, then roll back

```json
// functions_deploy  →  builds v2, makes it active
{ "name": "hello-world", "source": "export default async () => ({ v: 2 });" }

// functions_rollback  →  active version back to v1
{ "name": "hello-world", "version": 1 }
```

## What's deferred (post-v1.0)

- **python runtime** — needs its own harness; same socketpair
  protocol, so it's additive.
- **memory enforcement** — `max_memory_mb` is stored but not yet
  applied to the worker process (`prlimit`/cgroup).
- **`allowed_apps` allowlist** — `context.call` currently reaches any
  app as the functions app's identity; a per-function allowlist is
  the next hardening step.

## Local development

```bash
cd apps/mcp/functions
go build .
go test .                                # spawns real node workers
APTEVA_PROJECT_ID=test ./functions       # binds to :8080
```

Panel source is `ui/FunctionsPanel.tsx`; the worker harness is
`harness/node.mjs` (embedded into the binary). Rebuild the panel with
`bun run scripts/build-panels.ts` from `apps/`.
