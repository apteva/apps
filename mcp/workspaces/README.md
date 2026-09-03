# Workspaces

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
- Stop/resume, TTL extension, and confirmed permanent destruction.
- Automatic expiration: stop at `expires_at`, preserve the container and volumes for the
  configured recovery window, then delete them at `delete_at`.
- App-only creation with a bounded source archive and app-only source export.
- App-only origin/Git-safety context updates without taking ownership of Git.

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
docker build -t apteva/workspace-dev:0.1.0 images/apteva-dev
```

Then set the Workspaces app's `apteva_image` configuration to
`apteva/workspace-dev:0.1.0`.

## Source handoff

An authenticated originating app can call `workspace_create_for_resource` with
`source_archive_base64`, then later call `workspace_source_export`. Containers
validates and transfers the tar.gz archive. The current Containers defaults are
8 MB compressed, 128 MB expanded, and 20,000 archive entries.

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
- Background preview services and public preview routing.
- Live CPU/RAM telemetry (the backend currently returns limits and volume
  storage usage).
- Secret injection and selectable network policy.
- Snapshot/restore, full file browsing, checks, and Git operations.
