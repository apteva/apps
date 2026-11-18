Containers manages individual Docker workloads locally and on hosts supplied by the Instances app. Version 0.5.0 contains the audit fixes based on `containers/v0.4.0` and pins installation source to `containers/v0.5.0`. See [RELEASE-0.5.0.md](RELEASE-0.5.0.md) for changes and upgrade notes.

## v0.6.0

Sandbox features for running untrusted code (used by Workspaces).

- **Network modes.** `network_mode` on `containers_run`: `default` (the
  per-workload bridge network with internet, unchanged), `none` (no network
  interface besides loopback), or `registry_only` (an internal Docker network
  whose only route out is a per-workload tinyproxy that allows `egress_allow`
  hosts and denies everything else; default allow list `registry.npmjs.org`).
  Entries are `host`, `*.domain`, or `host:port`. Proxy variables
  (`HTTP(S)_PROXY`, `npm_config_*proxy`) are set on the container and on every
  execution. Direct connections cannot bypass the proxy because the internal
  network has no gateway. Published ports require `default`.
- **Switching between commands.** `containers_network_set` (app-only) moves a
  running workload between modes without restarting it, disconnecting before
  connecting so a stricter mode never overlaps a looser one. Typical flow:
  `registry_only` to install, then `none` to test. Non-default workloads are
  created stopped, moved to their network, then started.
- **Mounts.** `volumes[].read_only` mounts with `:ro`. `volumes[].shared_from`
  mounts the same-named volume of another live workload owned by the same
  caller on the same host (read-only only), which is how a test-phase workload
  reads what a prepare-phase workload wrote, without recreating the prepare
  workload. Borrowed mounts are never deleted, retained, or measured by the
  borrower. `tmpfs: [{mount_path, size_mb}]` adds size-limited tmpfs mounts.
  Injected files and archive imports into read-only volumes are rejected.
- **Hardening.** `hardened: true` drops all Linux capabilities, sets
  `no-new-privileges`, makes the root filesystem read-only (adding a 256 MB
  `/tmp` tmpfs so executions keep working), lowers the process cap to 256, and
  requires a non-root `user`. `read_only_rootfs` and `pids_limit` (16-4096,
  default 512) are also available on their own.
- **No Docker socket or platform token.** Workload volumes are always named
  volumes; a source that would become a host bind mount is refused. The
  sidecar's `APTEVA_APP_TOKEN` is rejected in env, files, and execution argv.
  Hardened workloads and their executions also reject `DOCKER_HOST` and
  `APTEVA_*` environment variables.
- **Process-tree kill.** Every execution and persistent session now marks its
  processes with `APTEVA_PROCESS_TREE`. Cancellation and session close kill
  marked processes too, so commands that `setsid` and double-fork away from the
  tree no longer survive. Processes that clear their environment are only
  reachable through the parent tree.
- **Tests.** Real-Docker tests prove `none` blocks traffic, `registry_only`
  allows the registry (including a real package install) and blocks other
  hosts, direct sockets, and look-alike domains, read-only and shared mounts
  reject writes, hardening removes capabilities, `pids_limit` is enforced,
  destroy removes the proxy and internal network, and cancellation reaches
  detached descendants.

Upgrade: migration `006_sandbox.sql` adds `read_only` and `shared_from` to
`containers_volumes`. Existing workloads keep `network_mode: default` and the
512 process cap. The first `registry_only` workload on a host builds the local
`apteva-containers-egress` image from `alpine:3.20` (needs internet once).


## v0.5.2

Source imports can update a running workspace after dependency installation.
Only destination paths touched by the archive are checked for symlinks, so
unrelated `node_modules/.bin` links are preserved. Writes through destination
symlinks remain rejected; the workload remains paused during validation and
copy. Replacing a file also breaks pre-existing hardlinks rather than modifying
the linked file. Real-Docker regressions cover all three cases.


## v0.5.1

Accept SDK `_project_id` routing metadata separately from the strict Docker
run specification. Ownership still comes from authenticated caller context;
all other unknown run fields remain rejected. SDK pinned to v0.77.0.


Run `RUN_CONTAINERS_TESTS=1 ./scripts/verify.sh` from this directory to run Go tests with the race detector, disposable Docker integration tests, vet, UI interaction tests, TypeScript checks, panel/source-map generation, and vulnerability scanning. Omit `RUN_CONTAINERS_TESTS=1` to skip Docker tests. Dependencies: Go 1.25.13 or newer, Bun, and a running local Docker daemon. Go commands use `GOWORK=off` so validation uses the pinned SDK v0.75.0, rather than a neighboring development checkout. Set `GOCACHE` to a dedicated writable directory if another task is clearing the shared cache.

Behavior and limits:

- HTTP requests use the selected install and trusted project identity supplied by the authenticated server proxy. Operators can manage all workloads in their authorized project; MCP callers additionally require matching app ownership. A missing project never grants wildcard access. The sidecar must remain behind the authenticated Apteva proxy.
- Omit `health_path` for container-state reporting alone. HTTP readiness checks are opt-in, with `health_scheme` (`http`/`https`) and optional `health_port`. Set `disable_health_check: true` to disable a blueprint's HTTP check. Probes run inside the workload's Docker network namespace. Docker's own image HEALTHCHECK still applies.
- Port `host_port: 0` asks the target daemon to allocate a port. The app records the resulting TCP/UDP binding. Remote loopback bindings are not advertised as public URLs.
- `destroy` preserves volume records unless `delete_volumes` is requested and deletion succeeds. To reuse one, supply `volumes: [{"name":"data","mount_path":"/data","retained_from":"wrk_..."}]`. The source must be destroyed and on the same host. Ownership transfers atomically; two replacements cannot claim the same volume. The panel supports reuse and subsequent deletion. Archive tools continue to resolve retained volumes. Metadata already erased by an older release is not guessed or automatically recreated: inspect the original workload's stored volume names and confirm Docker volume existence before repairing those historical records.
- Injected files retain private modes (default 0600) and inherit the workload UID/GID. `files[].owner` accepts an explicit numeric `uid:gid`. Images without a shell/user lookup utilities should provide an explicit owner. Named identities are resolved against the workload image, not the helper image.
- Execution and archive transfer are **local-only**. Execution requires POSIX shell utilities; native Windows and shell-less images are not supported. `argv` preserves the chosen executable and arguments. Use `shell_command` with `session_key` to preserve `/bin/sh` variables and current directory. Standard input is closed; interactive input is not supported. Commands may be multiline and up to 64 KiB; combined argv/environment is limited to 1 MiB.
- Shell state is process-local. On app restart, recorded running commands are reconciled and orphaned PTY process trees from this implementation are terminated. Legacy 0.4.0 PTYs did not record process-control identities; stopping/restarting those workloads is the reliable way to clear any pre-upgrade orphan processes. Detached services should redirect output and use workload supervision if they must survive session cleanup. Cancellation escalates to process-tree termination when necessary.
- Maximums are 16 concurrently creating workloads, 1,000 non-destroyed workloads, 64 active executions, and 64 shell sessions per app process. Idle sessions close after 30 minutes; operators and app owners can explicitly close idle sessions. Workload memory/CPU zero means unlimited, with a 512-PID ceiling. These controls are not a hardened sandbox for hostile workloads.
- Execution capture retains a bounded tail (1 MiB by default, at most 2 MiB), records the full captured byte count, and reports truncation. Temporary spool files can briefly require approximately twice the tail limit plus a small chunk. Workload logs use Docker's local driver with three 10 MiB rotated files. Output retention defaults to 24 hours. Failed runtime cleanup keeps its identity for retry; stopped workloads may need to resume before execution files can be deleted.
- Archive defaults are 8 MiB compressed, 128 MiB payload, and 20,000 entries, with bounded decompression, helper memory, and temporary storage. Imports reject traversal and pre-existing destination symlinks. Import/export briefly pause a running workload for a consistent operation; a durable resume record allows recovery after sidecar interruption. Imports are validated before writing but are not transactional filesystem snapshots: an I/O failure during the final copy can leave a partial import.
- Remote cancellation waits for the bounded Instances RPC to settle before cleanup. The current Instances API has no direct cancel operation. Failed/interrupted creates retain a labelled-container cleanup job for 20 minutes to catch late daemon results; transient cleanup failures keep the job for retry. A workload in this cleanup window cannot be restarted; create a replacement with a new identity. Retained volumes are never deleted by this late-result sweep.
- Blueprint omission inherits defaults; explicit empty ports/volumes/files/environment clears them, and an explicitly supplied zero resource object clears resource limits. Nonempty environment overrides merge by key. Workload and retained-volume lists are paginated; execution history exposes the latest 100 records and loads output on demand.
- Automatic route publishing, DNS, certificates, scheduled backups, and multi-container blueprint orchestration are not implemented by this app. Manifest integration hints now identify these as reserved integrations.

The additive `005_reliability.sql` migration adds execution shell semantics, query indexes, and durable archive/creation cleanup records. Back up the app database before deploying any release. See [FIXES.md](FIXES.md) for the audit mapping and validation evidence.
