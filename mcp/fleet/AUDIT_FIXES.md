# Fleet v0.10.5 audit patch

Base: `fleet/v0.10.5` (`608c9aebed61d314e40e1aeff2c6f572afe72b30`). Working branch: `fix/fleet-0.10.5-audit`. This is an unreleased patch; the published version and production installations have not changed.

## Changes against the audit

| Finding | Implemented behavior | Main regression coverage |
|---|---|---|
| F01 | Clones publish without replacing an existing directory. Cleanup removes only an operation-owned target and preserves failed cleanup state. New clone rows, operation ownership and quarantine commit together. | Existing-target preservation; `TestNewCloneIsBornLockedAndQuarantined` |
| F02 | Rooted Go extraction and bounded Python remote extraction reject path escapes, symlink ancestors, duplicate file entries and corrupt gzip trailers. Safe symlinks survive restore. Remote publication uses rename without replacement. | Malicious archive regressions; remote symlink round trip; corrupt gzip |
| F03 | Child environments use an allowlist. DNS uses a tenant-bound, revocable capability at `/dns/<tenant>/mcp`, with an explicit tool allowlist and trusted project context. Fleet's install token and master key are excluded. | Child environment tests; tenant-bound/revoked capability tests |
| F04 | Every protected Fleet HTTP route checks the install bearer even with `?sig`. Signed transfer and delegated endpoints authenticate independently. The companion SDK patch removes its generic signature-query bypass. | Real sidecar arbitrary-signature regression; SDK auth test |
| F05 | Public ingress probes carry no tenant admin bearer, require 2xx, reject redirects and check all DNS answers. Application routes are also checked through the authenticated tenant control endpoint. | Public-probe credential/404 regression and ingress tests |
| F06 | Systemd scope names use an exact hashed tenant slug, eliminating prefix collisions. Legacy processes use exact data-directory ownership checks. | Scope collision and process-ownership tests |
| F07 | Stop errors remain errors when ownership cannot be established. Completion checks include owned workers/scopes and the listening port; process reaping has one owner. Failed clone/restore cleanup blocks activation. | Unknown-listener regression; process-tree and Linux tests |
| F08 | Management reservations persist while tenants are stopped; SQL constraints prevent overlap with disjoint application blocks. New rows and initial reservations commit together. Existing listeners require identity validation; stopped tenants are not reactivated by startup reconciliation. | Legacy/stopped reservations, SQL overlap, actual spawn environment tests |
| F09 | Migration checkpoints source and target before stopping. Recovery fences both copies. Location and retained-source bookkeeping commit together; failed source cleanup remains retryable. | Durable restart/fencing and atomic migration commit tests |
| F10 | Provisioning, clones, lifecycle tools, startup recovery, health and A2A reconciliation coordinate through durable operations and re-read state while locked. | New-clone lock, controller restart and stale-health tests |
| F11 | Canonical ownership checks cover primary names, app hosts and wildcard grants; SQL triggers enforce conflicts across controllers. Existing primary links require explicit detach before replacement. Initial managed DNS assignment refuses to overwrite pre-existing provider records, preserving external DNS on rollback. | Duplicate primary regression; cross-purpose/wildcard SQL tests |
| F12 | Delete removes primary/app routing and owned DNS before deleting data/registry state. Failed cleanup retains metadata and a stopped tenant for retry. Revoke/detach retain records on provider failures. | Primary-ingress deletion regression and routing tests |
| F13 | Setup password and token are encrypted and saved before registration. Resume setup reuses the saved password and handles registration that already succeeded. | Partial-setup credential regression and recovery UI test |
| F14 | Key attachment validates the authenticated administrator identity endpoint and allows replacement. Key/setup state updates are transactional. | Invalid-key regression and handler tests |
| F15 | Setup completion is separate from process status. Stopped unfinished tenants retain recovery controls. Suspended remote monitoring can be resumed. | Setup-state regressions and browser test |
| F16 | Quarantine is a durable field, initialized before a clone becomes visible. Audit retention cannot lift it. | Retention/quarantine regression; new-clone test |
| F17 | Backup format 2 carries encrypted control state and runtime metadata. Snapshot restart uses the observed version, not a pending update. Restore stages on the destination filesystem, preserves symlinks, retains old data, and rolls back failed starts/credential checks. Interrupted restore recovery restores matching data and credentials. | Streaming restore, wrong-tenant archive and crash-recovery tests |
| F18 | Explicit missing binaries fail instead of falling back. The version dialog submits the pending target it displays. | Runtime-pin regression; exact-version browser test |
| F19 | Failure counters reset on success and persist outside audit events. Health responses must report success. | Consecutive-failure regression and health tests |
| F20 | Mount initializes the control plane; reconciliation runs as a worker afterwards. Unavailable hosts cannot prevent mounting. | Mount/reconciliation tests |
| F21 | Both RPC errors and MCP `isError` results propagate as failures in Go and the UI. | MCP regressions, Bun tests and browser error test |
| F22 | Verification changes diagnostic fields only. Ingress mode transitions are serialized; direct routes are removed through their actual owner before bookkeeping is removed. | Ingress tests and operation exclusion |
| F23 | Hosted launches receive scoped DNS and reserved app ranges. Host/version locks and remote `flock` serialize installation; unique staging directories are validated before publication. | Hosted environment tests; concurrent Linux installation test |
| F24 | Detail requests are abortable and guarded by tenant ID and generation. Polls coalesce; list requests are paginated. | Bun stale-response tests; rapid-selection browser test |
| F25 | The UI exposes operation IDs, phases, recovery and structured next steps. Conflicting controls are disabled. Templates provide a real project selector; setup recovery reveals credentials together. | Operation, rehearsal, project-picker and setup browser checks |
| F26 | `apteva.yaml` is the single embedded manifest; tests compare the complete parsed manifests. Stale bootstrap documentation was replaced. | Full manifest equality and panel build/import verification |

## Performance and retention

- A fixed pool of eight health workers replaces one goroutine per tenant. A 128-tenant test checks bounded concurrent requests.
- Installation/preparation locks are keyed by host and version. Unrelated resources can proceed independently; canceled waiters release their lock references.
- Snapshot transfer streams by default; legacy base64 snapshots/restores are capped at 32 MiB. Disk space is checked before snapshot staging. Filesystem cloning is attempted for ordinary files and SQLite is copied consistently. Compression and download do not extend the stopped window.
- HTTP tenant lists default to 50 records, cap pages at 200, and expose `has_more`. Clients paginate with `limit` and `offset`; the panel adds server-side search and coalesces polling. MCP/internal list behavior is unchanged.
- Logs retain five 10 MiB tails, checked every minute, on local and hosted tenants. Copy/truncate preserves descriptors across controller restarts; concurrent log writes can be lost in a small rotation window and growth can exceed the threshold between checks.
- The Local storage and cleanup panel shows free space and eligible local runtime/restore copies older than 30 days. Deletion requires selecting copies. Active versions and operations are rechecked; recovery data is not silently removed.

A synthetic 5,000-tenant benchmark on an Apple M1 Pro measured **25.37 ms / 14.85 MB allocated** for a full read plus JSON encoding versus **0.294 ms / 127 KB** for a 50-record page (five iterations each). These are local benchmark measurements, not production latency or backup-downtime guarantees.

## Validation and release boundaries

Validation includes Go unit, race and integration tests on macOS and Linux; SDK race tests; Go vet; TypeScript checking; Bun state tests; Chrome interaction tests with mocked APIs; and panel bundling/import verification. The Linux run executes the hosted installer concurrently using isolated fake package binaries. Detailed counts and transcripts are in the accompanying validation report.

No live VPS, tenant, DNS record or production credentials were changed. The tests do not establish complete fault coverage: live systemd/cgroup behavior, SSH interruption at every migration phase, provider-specific DNS propagation and large production backup downtime still need staging validation.

The companion SDK change is on `fix/fleet-signed-route-auth`, based on `app-sdk v0.73.0`. Fleet remains pinned to that published tag and is protected by its own route checks now; publish the SDK fix and update the dependency pin as part of release preparation. Existing signed-download consumers of the SDK must explicitly declare their signed routes `NoAuth` and verify signatures there.

Before rollout, back up Fleet's database/master key and use a fresh release version/ref. Apply migration 014, restart tenant processes to replace inherited environments, and rotate the old Fleet installation credential that previous tenant environments contained. Existing backups need the original master key to decrypt their control state. A DNS capability has no independent expiry: revocation uses a durable tenant epoch and live grant checks so long-running tenants do not lose delegation on a timer. Old hosted app-port assignments may need reconciliation before changing tenant environments. Hosted backup/restore and remote cache-retention management remain outside the existing local-backup feature set.
