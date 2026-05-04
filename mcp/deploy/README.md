# Deploy (v0.1)

Local-first builds and runtime supervision for Apteva projects.

Takes a code repo (from the **Code** app, a local path, or — later —
git/zip) and turns it into a built, supervised, URL-addressable
process running on the same host as Apteva. No external orchestrator,
no Docker required.

## Surfaces

- **9 MCP tools** — `deploy_init`, `deploy_list`, `deploy_get`,
  `deploy_build`, `deploy_release`, `deploy_status`, `deploy_logs`,
  `deploy_stop`, `deploy_destroy`
- **REST surface** at `/api/apps/deploy/api/*` for the dashboard panel
- **Deploy panel** — list of deployments, status cards, log tail,
  build/release/stop/destroy buttons
- **Event bus** — `deploy.created`, `deploy.build.{started,
  succeeded, failed}`, `deploy.release.{live, stopped, crashed,
  failed}`, `deploy.destroyed`

## Source kinds (pluggable)

| Kind     | `source_ref`                       | Status        |
|----------|------------------------------------|---------------|
| `code`   | Code app repo slug                 | v0.1 ✓        |
| `local`  | Absolute path on the deploy host   | v0.1 ✓        |
| `git`    | https://github.com/owner/repo[@ref] | v0.2          |
| `zip`    | uploaded zip id                    | v0.2          |

## Frameworks (pluggable)

| Framework | Build                                  | Runtime                          |
|-----------|----------------------------------------|----------------------------------|
| `go`      | `go build -o app . ` (CGO disabled)    | exec the compiled binary         |
| `static`  | (none) or `build_cmd` → `dist/`        | in-process `http.FileServer`     |
| `blank`   | optional `build_cmd`                   | requires `start_cmd`             |

Auto-detected from the source tree (`go.mod` → `go`, `index.html` →
`static`, etc.) when `framework` is empty.

## Runtime targets (pluggable)

v0.1 ships a single `LocalRuntime` that supervises the build artifact
as a child process — port allocated from a configurable range,
stdout/stderr captured to a per-release log file, TERM-then-KILL
shutdown, in-process FileServer for static deployments.

The `Runtime` interface is the seam: `DockerRuntime` (isolation +
resource caps) and `SSHRuntime` (deploy to a VPS by `scp` + `ssh`)
plug in behind the same interface in v0.2 with no schema or surface
changes.

## Local development

```bash
cd mcp/deploy
go build .
APTEVA_PROJECT_ID=test DEPLOY_DATA_DIR=/tmp/deploy ./deploy
curl http://localhost:8080/health
```

## Tests

```bash
go test ./...                       # tier 1, ~20ms
go test -tags integration ./...     # tier 2, ~6s — real binary, real build, real HTTP
apteva test ./scenarios/            # tier 3, ~3min — real LLM
```

Tier 2 spawns the deploy sidecar, materialises a tiny Go fixture in
a temp dir, then runs the full
init→build→release→fetch→stop→destroy round-trip. Requires `go` on
PATH (the build step shells out to it).

## Storage layout

```
/data/deploy.db                                metadata
/data/builds/<build_id>/src/                   unpacked source
/data/builds/<build_id>/dist/                  build output
/data/builds/<build_id>/build.log              build stdout/stderr
/data/releases/<release_id>/runtime.log        runtime stdout/stderr
```

## Configuration

| Key                     | Default                  | Notes                                   |
|-------------------------|--------------------------|-----------------------------------------|
| `port_range_start`      | `7000`                   | First port the supervisor may assign    |
| `port_range_end`        | `7999`                   | Last port the supervisor may assign     |
| `max_build_concurrency` | `2`                      | Hard cap on simultaneous builds         |

The `code` source kind reaches the Code app over `PlatformClient.CallApp`
(MCP `repos_export`); install-time binding to a code app fills the
`code` role declared in this app's manifest.

## Out of scope for v0.1

- Docker / container builds — `LocalRuntime` only
- Remote deploy targets — `SSHRuntime` lives in v0.2
- Custom domains — deployments are reachable via the auto-allocated
  port; routing under `/_deploy/<name>/` lands in a follow-up
- Build caches — every `deploy_build` is a cold build
- Preview environments — one live release per deployment
- Resource caps (CPU/mem) — supervised process inherits the host's
  limits; add via `setrlimit` when it matters
