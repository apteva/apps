# Workspaces

## v0.8.1

Fixes creates that failed and then blocked every retry with
`UNIQUE constraint failed: workspaces.project_id, workspaces.name`.

- A create that fails (container start, source import, or a test workspace
  setup step) removes what it started and marks the workspace `destroyed`
  with the error kept in `last_error`, so its name is free again.
- `workspace_create_for_resource` is idempotent per caller and resource
  (`resource_kind` + `resource_id`), not only per tool call. A repeat call
  returns the existing workspace with `reused: true`: running ones as they
  are, stopped or expired ones extended if needed and resumed, and ones that
  failed after they had run resumed rather than replaced, because their
  volumes may hold work. A workspace for that resource that never became
  ready is cleared and a new one is created.
- Failed records left by 0.8.0 that never became ready are cleared the next
  time the same caller creates a workspace with that name.
- A workspace stuck in `provisioning` for 15 minutes (for example after a
  restart mid-create) is cleaned up by the reconcile worker.
- When another caller's live workspace holds the name, the error names that
  workspace and its status instead of the database error.

## v0.8.0

Requires Containers 0.7.1 or later.

**Test workspaces** (`kind: test`) let an app run an untrusted test suite in a
locked-down workspace. One workspace is one container.

- App-only tools: `workspace_test_create`, `workspace_results_export` and
  `workspace_source_digest`. Agents never see test workspaces, and other apps
  cannot open one. Operators can limit which apps may create them with
  `test_workspace_apps` (empty allows any installed app).
- The container is hardened by Containers: no capabilities,
  no-new-privileges, read-only root, at most 256 processes, a writable `/tmp`
  tmpfs (`test_tmp_mb`, default 512 MB, holds `HOME`), and CPU and memory
  limits for the whole workspace (`test_cpu`, `test_memory_mb`). Local hosts
  only.
- No shell session, no previews, no file tools and no source sync or export.
  `workspace_command_start` takes `argv` only.
- Two numeric users replace read-only mounts. `builder` (1000:1000) owns the
  source in `/workspace/src` and the dependency cache in `/cache/bun`.
  `runner` (1001:1001) owns only `/workspace/out` (`$TESTS_OUT`) and can read,
  but not write, source and cache. Without capabilities neither user can get
  around file permissions. Nobody can write the volume roots.
- Each command picks `user` (`builder` or `runner`, default `runner`) and
  `network` (`none`, `registry_only` or `default`, default `none`).
  Workspaces switches the container's network before the command starts;
  only one command runs at a time, so the setting cannot leak into another
  one. `registry_only` reaches `test_egress_allow` hosts (default
  `registry.npmjs.org`). Every command result names the digest-pinned image
  it ran in. A typical run: `bun install` as `builder` with `registry_only`,
  then `bun test` as `runner` with `none`.
- Expiry: default 30 minutes, at most 120 (`test_default_ttl_minutes`,
  `test_max_ttl_minutes`). An expired test workspace is destroyed at once,
  without the retention window regular workspaces get.
- `workspace_results_export` returns `/workspace/out` as tar.gz (8 MB
  compressed limit) and then destroys the workspace unless `destroy=false`.
  Only that folder is collected. Docker volumes cannot enforce a disk quota,
  so the export limit is the bound on results.
- `workspace_source_digest` returns the tree digest Workspaces computed from
  the archive it imported, never from inside the container. Method
  `workspaces-tree-v1`: sha256 over sorted lines `path NUL type NUL mode NUL
  sha256` for every file and symlink (type `f` or `l`; mode 755 for
  executables, 644 otherwise, 777 for symlinks; for symlinks the hash is of
  the link target). Folders are implied by files, and anything under
  `node_modules` is left out.
- Dependency cache: one standalone Containers volume per project and lockfile
  hash (the lockfiles at the archive root: `bun.lock`, `bun.lockb`,
  `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`). Without a lockfile, or
  with `use_cache=false`, the workspace gets its own cache that is deleted
  with it. Caches unused for `test_cache_retention_days` (default 7) are
  deleted.
- Profile `bun-test` uses `bun_test_image`, which must be digest pinned
  (default `oven/bun:1-debian` at a fixed digest, Bun 1.4.4).

**Regular workspaces:** commands and previews now run with umask 022, so
files and folders they create are readable by others (0644 and 0755) instead
of private to their user.

## v0.7.0

Agents can use a workspace as their computer without shell workarounds:

- `workspace_file_read`, `workspace_file_write`, `workspace_apply_edits` and
  `workspace_file_list` move exact text through Containers' volume archive
  transfer (2 MB text files) and support SHA-256 preconditions. Listing runs a
  short helper outside the agent's PTY session and prunes `.git`,
  `node_modules` and `.home` by default.
- `workspace_source_import` lets agents unpack a tar.gz into the workspace.
- `workspace_command_wait` blocks up to 20 s and returns output since an opaque
  cursor; `workspace_command_output` reads the same window without waiting.
  With Containers 0.5.2, command output is published when the command
  finishes, so while it runs these return status with empty output.
- Preview tools are no longer app-only, and `workspaces_create` accepts
  `preview_port`. `workspace_preview_stop` gains `keep_workspace_running`,
  defaulting to true for agents (preview process only) and false for app
  callers, so Code keeps its v0.6 behaviour.

New files are written as 0644 and existing files keep their mode and owner.
With Containers 0.5.2 missing parent directories were created as 0700; since
v0.8.0 (Containers 0.7.1) they are 0755.

File transfers pause the container for the duration of the copy, as every
Containers volume archive operation does.


## v0.6.2

Preview Stop succeeds when the workspace was already destroyed, allowing Code
to restart or delete the repository after workspace retention cleanup. Requires
Containers 0.5.2 for source updates alongside dependency caches.


## v0.6.1

Requires Containers 0.5.1 for compatibility with SDK project-routing metadata.


## v0.6.0

App callers can create a workspace with `preview_port` and use
`workspace_preview_start/get/logs/stop` to manage an HTTP preview. Containers
publishes an automatically allocated loopback port; the process binds
`0.0.0.0` inside Docker. Preview executions have a separate lifecycle from the
command PTY, bounded logs, readiness checks, a ten-minute startup deadline, and
the workspace TTL as their maximum lifetime. Stop halts the container and
retains volumes under the workspace retention policy. Handles persist through
Code and Workspaces restarts. Requires Containers 0.5.0 or later.


## v0.5.0

- Accept an operator-allowlisted, digest-pinned image per workspace while
  retaining profiles for toolchain and lifecycle policy.
- Let Code forward repository and command image preferences without giving it
  direct access to the Containers runtime.
- Publish the multi-architecture Apteva development image definition with
  persistent language caches and login-shell-safe tool paths.

## v0.4.0

- Add an app-only, Git-agnostic source round trip: create with a producer
  revision, refresh managed files without deleting dependency caches, export a
  filtered source archive, and acknowledge the revision accepted upstream.
- Keep optimistic revision checks and the originating app-install boundary on
  every source operation. Workspaces still does not commit, merge, or push.

## v0.3.0

- Give every workspace a named, long-lived PTY shell session. `cd`, exported
  variables, shell functions, and aliases now carry into later commands.
- Keep command history, bounded live logs, timeouts, and targeted interruption
  while running commands through the same shell.

## v0.2.0

- Run every command inside the workspace's persistent container. Installed OS
  packages and writable-layer files survive between commands and stop/resume;
  background services remain available to later commands while it is running.
- Keep asynchronous command logs, cancellation, attribution, and lifecycle
  auditing without creating disposable command containers.

## v0.1.1

- Keep every native panel API request pinned to the selected project and app
  install so project-scoped installations resolve correctly.

Workspaces is the control room for local development environments. It owns
workspace identity, assignment, TTL, activity, commands, safety context, and
the operator UI. The required Containers app owns Docker workloads, named
volumes, in-container execution, cancellation, bounded logs, and storage usage.

The product boundary is deliberate:

- **Code** owns repositories, editing, Git, issues, commits, and pushes.
- **Workspaces** owns where code runs, who owns it, what is executing, and when
  the environment expires.
- **Containers** is the low-level runtime and remains useful as an operator
  debugger; Workspaces does not expose raw Docker controls.

## Current surfaces

- Workspace list with derived running/busy/suspended/failed state and TTL.
- Workspace detail with ownership, origin, approved image, resource limits,
  runtime health, durable volumes, storage usage, and activity.
- Stateful PTY-backed asynchronous command history with bounded logs, exit
  status, duration, actor attribution, and cancellation.
- Agent file tools: line-window reads, whole-file writes, exact-string edits
  with SHA-256 preconditions, directory listing, and archive import.
- Blocking command wait and cursor-based incremental output.
- HTTP previews on a published loopback port, for agents and app callers.
- Stop/resume, TTL extension, and confirmed permanent destruction.
- Automatic expiration: stop at `expires_at`, preserve the container and volumes for the
  configured recovery window, then delete them at `delete_at`.
- App-only creation, exact managed-source refresh, filtered source export, and
  accepted-revision tracking.
- App-only origin/Git-safety context updates without taking ownership of Git.
- App-only test workspaces: hardened container, builder and runner users,
  per-command network, source digest, results export, per-project dependency
  cache.

Only one tracked command may execute in a workspace at a time. Commands share a
long-lived PTY shell inside the persistent container, so `cd`, exported
variables, shell functions, and aliases carry into later commands. `/workspace`,
`/cache`, installed packages, and writable-layer files survive stop/resume.
Stopping the workspace terminates its processes and PTY; resume starts a fresh
shell in the same preserved container filesystem.

## Runtime profiles

The ready-to-use profiles map to operator-configured images:

| Profile | Default image |
|---|---|
| `go` | `golang:1.25-bookworm` |
| `bun` | `oven/bun:1-debian` |
| `python` | `python:3.13-bookworm` |
| `apteva` | disabled until `apteva_image` is configured |

The combined Apteva image definition lives under
`images/apteva-dev/Dockerfile`. Build it locally with:

```bash
docker build -t apteva/workspace-dev:0.1.1 images/apteva-dev
```

The published multi-architecture image is available as
`ghcr.io/apteva/workspace-dev:0.1.1`. Prefer its immutable digest when using it
as a profile image or per-workspace override.

Workspace creation may also pass an explicit `image`. Explicit images are
validated against `custom_image_prefixes` and, by default, must use an
immutable `@sha256` digest. The requested image overrides the selected
profile's configured image; the profile still describes the expected
toolchain. Workspaces records the exact image and passes it unchanged to
Containers.

## Source handoff

An authenticated originating app can call `workspace_create_for_resource` with
`source_archive_base64`, `source_digest`, and `source_paths`. It can later call
`workspace_source_sync` with `expected_source_digest` to refresh only the files
it manages. Paths removed from the new manifest are deleted; dependency caches,
the workspace home, and unrelated files are preserved.

`workspace_source_export` with `managed=true` builds a filtered source archive
that excludes Git metadata, workspace state, dependencies, and common build
outputs. After reconciling that archive into its source of truth, the producer
calls `workspace_source_accept` to advance the recorded revision. Containers
validates and transfers all tar.gz archives. The current limits are 8 MB
compressed, 128 MB expanded, and 20,000 entries.

Source-capsule producers should normalize archive file ownership to uid/gid
1000 so the non-root combined Apteva profile can edit imported files.

Workspaces does not interpret Git metadata. Code remains responsible for
reconciling an exported workspace snapshot with its authoritative repository.
`workspace_context_update` lets Code report labels plus `dirty_state` and
`unpushed_state`; these are displayed as consumer-reported facts during
destruction.

## Local development

```bash
go test ./...
bun run scripts/build-panels.ts --app workspaces
```

The root `go.work` overlays the local app SDK. `go.mod` remains pinned to the
latest SDK tag so source installation outside the monorepo resolves correctly.

## Deferred

- Remote execution and archive transfer (Containers currently supports these
  two operations only for local workloads).
- Interactive PTY sessions.
- Public preview routing (previews are loopback-only on the Apteva host).
- Live CPU/RAM telemetry (the backend currently returns limits and volume
  storage usage).
- Secret injection, and network policy for regular workspaces (test
  workspaces choose the network per command).
- A `browser-test` profile (Bun plus Chromium) for test workspaces; it needs
  an image built and published under `ghcr.io/apteva/`.
- Sharing the source digest method with Deploy as one Go package.
- Snapshot/restore, a file browser in the panel, binary file transfer through
  the agent tools, checks, and Git operations.
