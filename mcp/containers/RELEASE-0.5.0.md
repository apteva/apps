# Containers 0.5.0

This release addresses the 22 findings from the Containers 0.4.0 audit, including Docker argument injection, project isolation, lifecycle and execution races, cancellation recovery, retained-volume management, PTY reliability, output limits, archive safety, file ownership, port allocation, health checks, and UI behavior.

Installation source is pinned to `containers/v0.5.0`. The app uses app-sdk 0.75.0, Go 1.25.13 or newer, and x/sys 0.44.0.

## Changes

- Enforce HTTP project boundaries and MCP app ownership; qualify panel requests by installation and project.
- Coordinate workload lifecycle operations, persist execution identity before startup, retry cancellation and cleanup, and prevent stale observations from overwriting completed results.
- Preserve retained volumes, support exclusive reuse and later deletion, and add execution/session management to the panel.
- Support Alpine shells, long/multiline commands, explicit Bash argv and correct exit status; bound execution capture and rotate Docker logs.
- Allocate published ports through the target Docker daemon, make HTTP health checks explicit, and resolve injected-file ownership against the workload user.
- Bound archive processing, reject unsafe paths and symlinks, and recover paused workloads after interrupted transfers.
- Add scoped pagination, session/admission limits, persistent action errors, secret-free quick presets, validated forms and accessible dialogs.

## Upgrade notes

- HTTP readiness checks for new workloads are opt-in: supply `health_path`, optionally `health_scheme` and `health_port`. Use `disable_health_check: true` to disable a blueprint's HTTP check. Existing stored HTTP checks and Docker image HEALTHCHECK behavior are preserved.
- Explicit `argv` is executed literally. For shell variables/current directory to persist, use `shell_command` with `session_key`; named state uses `/bin/sh`. Sessions close after 30 idle minutes and do not survive an app restart.
- The additive `005_reliability.sql` migration adds execution semantics, indexes, and durable cleanup records. Back up the app database before upgrading.
- Version 0.4.0 could erase retained-volume metadata and leave PTYs without process-control identities. Historical volume associations require verification before repair; stop/restart affected workloads to clear pre-upgrade orphan processes. The release does not guess deleted metadata or restart user workloads automatically.
- Execution and archive transfer remain local-only. Remote operations still use the Instances API; cancellation waits for that bounded RPC, and late failed creates receive a cleanup sweep.
- Automatic routes, DNS, certificates, scheduled backups and multi-container blueprint orchestration remain outside this app's implemented feature set.

## Validation

85 Go tests passed with race detection, including disposable Docker tests on Alpine 3.20 and oven/bun:1-debian. Seven React UI interaction tests, TypeScript, Go vet, production panel/source-map checks, Go vulnerability scanning and the Bun dependency audit passed. Release checks also validate the version/source pin and Linux amd64 compilation.

No live remote VPS or production upgrade smoke test was performed. This release was published without invoking an upgrade of existing installations.

[Full remediation mapping](https://github.com/apteva/apps/blob/containers/v0.5.0/mcp/containers/FIXES.md) · [Operational details](https://github.com/apteva/apps/blob/containers/v0.5.0/mcp/containers/README.md)
