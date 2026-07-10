# Deploy

Build and release services, Android apps, and iOS apps for Apteva projects.

Takes a code repo (from the **Code** app, a local path, or — later —
git/zip) and turns it into a built, supervised, URL-addressable
process running on the same host as Apteva. No external orchestrator,
no Docker required.

## Surfaces

- **MCP tools** for deployments, environments, builds, releases,
  promotions, rollouts, domains, logs, retention, and health
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

| Framework | Build                                                  | Runtime                                  |
|-----------|--------------------------------------------------------|------------------------------------------|
| `go`      | `go build -o app . ` (CGO disabled)                    | exec the compiled binary                 |
| `node`    | `<pm> install` + `<pm> run build` (if defined)         | `<pm> run start` (override via start_cmd) |
| `static`  | (none) or `build_cmd` → `dist/`                        | in-process `http.FileServer`             |
| `blank`   | optional `build_cmd`                                   | requires `start_cmd`                     |
| `android` | Gradle bundle task or `build_cmd` -> signed `.aab`       | Google Play track                        |
| `ios`     | Xcode archive/export or `build_cmd` -> `.ipa`            | TestFlight or App Store                  |

Auto-detected from the source tree (`go.mod` → `go`, `package.json` →
`node`, `index.html` → `static`, etc.) when `framework` is empty. The
node builder picks `<pm>` from lockfiles in priority order:
`bun.lockb` → bun, `pnpm-lock.yaml` → pnpm, `yarn.lock` → yarn,
otherwise npm.

## Mobile releases

Set `target_kind` and `framework` to `android` or `ios`. Mobile targets
reuse the existing deployment -> environment -> build -> release records;
they do not create a parallel deployment model.

Android `target_config_json` supports:

```json
{
  "package_name": "com.example.app",
  "module": "app",
  "variant": "release",
  "gradle_args": []
}
```

The default build runs `:app:bundleRelease`. Bind `play_store` to a
Google Play Developer connection for publishing. Gradle may sign the
bundle itself; otherwise bind `android_signing` to an Android Upload
Signing connection containing the encrypted base64 keystore, passwords,
and key alias. AAB uploads stream from disk and retry once after a
platform-managed OAuth refresh.

iOS `target_config_json` supports:

```json
{
  "bundle_id": "com.example.app",
  "scheme": "App",
  "team_id": "TEAMID",
  "version_name": "1.2.3",
  "build_number": "42",
  "app_store_app_id": "123456789"
}
```

Bind `app_store` to an App Store Connect connection containing Issuer ID,
Key ID, and the `.p8` private key. The default build requires macOS and
Xcode and uses automatic provisioning. Release channels are `internal`
or `external` TestFlight and `production` App Store. Deploy polls App
Store processing, assigns the processed build to a TestFlight group or
App Store version, and can submit the production version for review.

Use `deploy_promote` to move the same Android version code or iOS build
from a test channel to production without rebuilding. `deploy_rollout`
changes a staged Google Play production fraction; `deploy_halt` halts a
Play rollout or expires a TestFlight build.

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
/data/releases/<release_id>/runtime.log        runtime or store release log
```

## Configuration

| Key                     | Default                  | Notes                                   |
|-------------------------|--------------------------|-----------------------------------------|
| `port_range_start`      | `7100`                   | First port the supervisor may assign (skips macOS AirPlay on 5000/7000) |
| `port_range_end`        | `7999`                   | Last port the supervisor may assign     |
| `max_build_concurrency` | `2`                      | Hard cap on simultaneous builds         |

The `code` source kind reaches the Code app over `PlatformClient.CallApp`
(MCP `repos_export`); install-time binding to a code app fills the
`code` role declared in this app's manifest.

## Out of scope for v0.1

- Docker / container builds — `LocalRuntime` only
- Remote deploy targets — `SSHRuntime` lives in v0.2
- Build caches — every `deploy_build` is a cold build
- Resource caps (CPU/mem) — supervised process inherits the host's
  limits; add via `setrlimit` when it matters
