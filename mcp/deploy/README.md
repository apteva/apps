# Deploy

Build and release services, Android apps, and iOS apps for Apteva projects.

Takes a code repo (from the **Code** app or a local path) and turns it
into a built, supervised, URL-addressable process. Builds can run on
the Deploy host, a Git-independent Apteva capsule runner, Codemagic,
or GitHub Actions.

## Surfaces

- **MCP tools** for deployments, environments, builds, releases,
  promotions, rollouts, domains, logs, retention, and health
- **REST surface** at `/api/apps/deploy/api/*` for the dashboard panel
- **Deploy panel** — list of deployments, status cards, log tail,
  build/release/stop/destroy buttons
- **Event bus** — `deploy.created`, `deploy.build.{started,
  succeeded, failed, cancelled}`, `deploy.release.{live, stopped, crashed,
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

## Cloud build backends

Build execution is selected per environment and snapshotted onto each
build. Existing deployments default to `local`.

### Git-independent capsule runner

The `runner` backend sends a complete, signed source capsule directly to
another Deploy binary running in capsule-runner mode. It does not create,
clone, or require any Git repository. The runner verifies the capsule's
exact size and SHA-256 before extraction, uses Deploy's existing builders,
and returns a ZIP artifact over its authenticated HTTP API.

Start a runner on a macOS build host:

```bash
export APTEVA_RUNNER_TOKEN="$(openssl rand -hex 32)"
./deploy --capsule-runner \
  --listen 127.0.0.1:9075 \
  --data-dir "$HOME/.apteva/deploy-runner" \
  --concurrency 1
```

Expose it through HTTPS for a remote Deploy installation. Plain HTTP is
accepted only for a loopback runner. Configure an environment with:

```json
{
  "runner_url": "https://build-mac.example.com",
  "source_base_url": "https://apteva.example.com",
  "source_url_ttl_seconds": 7200,
  "artifact_mode": "file",
  "artifact_name": "apteva-build"
}
```

Set `build_backend` to `runner`. Configure the same secret in Deploy's
`runner_token` setting and the runner's `APTEVA_RUNNER_TOKEN` environment
variable. The Deploy sidecar may also receive `APTEVA_RUNNER_TOKEN` directly;
`runner_token_env` can select another environment-variable name. The token is
never stored in deployment or build records.

The runner accepts jobs idempotently, supports status, cancellation, logs,
and authenticated artifact retrieval, retains terminal job output for 24
hours, and marks interrupted builds failed after a runner restart. Source
capsules remain owned by Deploy and are removed when the build reaches a
terminal state.

For an unsigned iOS compile test, add `"smoke_only": true` to
`target_config_json`. A signed iOS job receives only the required App Store
Connect signing fields from Deploy's bound `app_store` connection. Android
jobs receive upload-key fields from `android_signing` when available. Store
publishing still happens through Deploy after it retrieves the IPA or AAB.

### Integration-backed providers

Bind Codemagic or GitHub integrations to the `cloud_build` role before using
their respective backends.

Codemagic example:

```json
{
  "app_id": "codemagic-app-id",
  "workflow_id": "ios-release",
  "branch": "main",
  "instance_type": "mac_mini_m4",
  "artifact_mode": "store_upload",
  "groups": ["ios-signing", "app-store"]
}
```

For iOS, `deploy_mobile_signing_setup` automates the provider setup that can
be automated:

1. Register or reuse the Apple Bundle ID.
2. Wait with `action_required` until the operator creates the App Store
   Connect app record for that Bundle ID.
3. Generate a new RSA key and CSR in memory.
4. Create an Apple Distribution certificate and App Store provisioning
   profile.
5. Deliver the App Store Connect API key and certificate private key directly
   to the selected build provider's secure secret store.
6. Add the provider secret group to this environment's build configuration.

Deploy stores only resource IDs, the provider secret reference, the public-key
fingerprint, and status. Private keys are never written to Deploy's database.
Calling setup again is idempotent; pass `rotate: true` to create and activate a
replacement before removing the old Apple profile and certificate.

Codemagic is the first `mobileSigningProvider` adapter. Additional build
providers implement the same secret-delivery interface while reusing the
provider-independent Apple lifecycle. A provider must expose secure secret
CRUD through its integration before Deploy can automate signing for it.

Apple still requires an operator to accept agreements, create/download the
initial App Store Connect API key, and create the App Store app record. App
privacy, legal, review, and required store metadata also remain operator-owned.

Codemagic builds remain repository-backed because Codemagic's API requires an
application, workflow, and branch or tag. Its legacy `source_mode: bundle`
option remains accepted for compatibility with operator-managed workflows,
but the Git-independent path is the `runner` backend above.

GitHub Actions example:

```json
{
  "owner": "acme",
  "repo": "mobile-app",
  "workflow_id": "build.yml",
  "ref": "main",
  "artifact_mode": "file",
  "artifact_name": "apteva-build",
  "artifact_file": "app-release.aab",
  "inputs": {"configuration": "release"}
}
```

`artifact_mode` defines the provider-neutral output contract:

| Mode | Contract |
|------|----------|
| `bundle` | The named ZIP artifact is unpacked as the service/static build output. |
| `file` | The named artifact is staged as one file; use for Android AAB or iOS IPA. Runner/GitHub ZIP containers are unpacked and `artifact_file` selects the output when they contain multiple files. |
| `store_upload` | The workflow signs and uploads iOS to App Store Connect; Deploy adopts it and continues TestFlight/App Store processing. |
| `none` | The workflow has no deployable output. |

The capsule runner serves authenticated artifact and log routes directly.
Codemagic reads short-lived artifact URLs from the completed build.
GitHub Actions expects a workflow artifact named `apteva-build` unless
overridden. GitHub workflow inputs are passed exactly as configured, so
the workflow must declare each configured `workflow_dispatch` input.
The `cloud_build_sync` worker polls providers, persists provider job
IDs/status, stages outputs, and performs a requested release only after
the build succeeds. `deploy_build_cancel` and
`POST /api/builds/:id/cancel` cancel active provider jobs.

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
/data/builds/<build_id>/source.zip             active cloud-build source capsule
/data/builds/<build_id>/source-capsule.json    capsule hash and expiry
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
