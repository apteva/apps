# Functions (v0.1)

Lambda-style serverless functions for Apteva. Each function gets an
auto-routed HTTP endpoint at `/fn/<name>`; the dispatcher spawns the
configured runtime once per invocation with `stdin = event JSON`,
captures `stdout` as the response, and kills the process group on
timeout. No long-running processes, no build step beyond writing the
source to a temp dir.

## What's in v0.1

- **Four runtimes:** `bun` (TypeScript), `node` (JavaScript/ESM),
  `python` (Python 3), `sh` (POSIX shell). The binary is resolved via
  `PATH` at invoke time — a missing runtime fails the function, not
  the sidecar.
- **Two source kinds:** `inline` (body stored on the function row) and
  `repo` (an entry file in a Code-app repo, fetched via
  `code_read_file` and cached by source hash).
- **Three triggers:** HTTP (`POST /fn/<name>`), cron (pair with the
  Jobs app), and manual (`functions_invoke` MCP tool or the panel).
- **8 MCP tools:** `functions_create`, `functions_update`,
  `functions_delete`, `functions_list`, `functions_get`,
  `functions_invoke`, `functions_invocations`, `functions_logs`.
- **REST surface** at `/api/apps/functions/*` for the dashboard panel
  and for other apps.
- **Functions panel** (native React) in the `project.page` slot —
  list, create, an inline invoke console, source/env view, and the
  recent-invocations log.
- **Per-invocation isolation:** fresh temp dir as cwd, capped stdout
  (64 KB) / stderr (16 KB), hard timeout, process-group kill so
  children don't outlive the leader.
- **Per-function concurrency cap** (8) so a runaway caller can't
  fork-bomb the host on one function.
- **Two install scopes:** `project` (one install per Apteva project)
  or `global` (one install across projects, isolated by `project_id`).

## The function contract

Every runtime sees the same shape:

- The **event** arrives as a single JSON value on `stdin`. An empty
  body is `null`.
- Whatever the function writes to **stdout** is the response. If it
  parses as JSON the HTTP trigger tags it `application/json`,
  otherwise `text/plain`.
- A non-zero exit, a crash, or a timeout marks the invocation
  `error` / `timeout`; `stderr` is captured either way.
- Three env vars are injected: `APTEVA_FUNCTION_NAME`,
  `APTEVA_FUNCTION_ID`, `APTEVA_FUNCTION_RUNTIME`, plus anything in
  the function's own `env` map.

## Simple examples

### Create an inline `bun` function

```bash
POST /api/apps/functions/functions?project_id=proj-1
{
  "name": "hello-world",
  "runtime": "bun",
  "source_kind": "inline",
  "source": "const event = await Bun.stdin.json();\nconsole.log(JSON.stringify({ hello: event?.name ?? \"world\" }));"
}
```

Invoke it over HTTP — the request body is the event:

```bash
curl -X POST https://<host>/api/apps/functions/fn/hello-world \
  -H 'Content-Type: application/json' \
  -d '{"name":"Marco"}'
# → {"hello":"Marco"}
```

### The same thing as an MCP tool call

```json
// functions_create
{
  "name": "hello-world",
  "runtime": "python",
  "source": "import sys, json\nevent = json.load(sys.stdin)\nprint(json.dumps({\"hello\": event.get(\"name\", \"world\")}))"
}

// functions_invoke
{ "name": "hello-world", "event": { "name": "Marco" } }
// → { "status": "ok", "response": "{\"hello\": \"Marco\"}", "duration_ms": 41, ... }
```

### A `sh` one-liner that echoes its event

```json
// functions_create
{
  "name": "echo",
  "runtime": "sh",
  "source": "event=$(cat)\necho \"{\\\"received\\\": $event}\""
}
```

### Run a function on a schedule (with the Jobs app)

Functions has no scheduler of its own — pair it with Jobs:

```bash
POST /api/apps/jobs/jobs?project_id=proj-1
{
  "name": "nightly-rollup",
  "schedule": { "kind": "cron", "cron": "0 3 * * *" },
  "target": { "kind": "http", "app": "functions", "path": "/fn/nightly-rollup" }
}
```

### Point a function at a Code-app repo instead of inline source

```json
// functions_create
{
  "name": "report",
  "runtime": "node",
  "source_kind": "repo",
  "repo_id": 12,
  "repo_path": "functions/report.mjs"
}
```

The body is fetched from the Code app at create time (for hashing)
and on the first invocation, then cached by `(repo_id, repo_path,
source_hash)` until the source changes.

## What's deliberately deferred

- **No memory enforcement.** `max_memory_mb` is stored but not yet
  applied to the spawned process — treat it as advisory in v0.1.
- **No network/filesystem sandbox.** Functions run as ordinary host
  subprocesses. Author them as trusted code.
- **No deploy integration.** Functions never become long-running
  supervised releases — if you need that, use the Deploy app.
- **No async invocation.** Every invoke is synchronous; for
  fire-and-forget, schedule it through Jobs.

## Local development

```bash
cd apps/mcp/functions
go build .
APTEVA_PROJECT_ID=test ./functions      # binds to :8080
curl http://localhost:8080/health
```

The panel source is `ui/FunctionsPanel.tsx`; rebuild the `.mjs`
bundle with `bun run scripts/build-panels.ts` from `apps/`.

See `migrations/` for the schema and `main.go`'s `MCPTools()` for the
full tool surface.
