# Apteva Code 0.9.0 — remediation of the 0.8.2 audit

Release 0.9.0 remediates the audit of `code/v0.8.2` at
`c2bf4d352ecdf06ae6ee3a80f6e5c7ef06b44e39`. Validation used disposable local
repositories, databases, processes and a Git server; installed production
runtimes and user data were not modified.

The original 16 failing regressions are retained in `audit_regression_test.go`.
Additional behavioral tests live in `reliability_test.go`, `recovery_test.go`
and `ui/tests/editor.spec.ts`. Source comments and README describe the new
contracts and operational limits.

## Changes by original finding

| Audit | Implemented remediation | Main evidence |
| --- | --- | --- |
| 1 | Repository transactions cover read–validate–write, Git, imports and finite local commands; conditional replacements use hashes. | Concurrent edits repeated 100 times; shared Git/edit lock; HTTP conflict test |
| 2 | Create patches and conditional creates reject occupied paths, including dangling symlinks. | Existing-file and dangling-symlink regressions |
| 3 | Batch mutation plan validates conflicts/budgets, journals originals and rolls back failures. | Patch/ZIP failure rollback; unmanaged-child rollback; aggregate-limit rejection |
| 4 | Rename rejects occupied destinations and supports same-file case changes. | Original no-overwrite regression |
| 5 | Git machine output retains whitespace/NUL records; stderr is separate and overflow is explicit. | Unstaged one-character path regression; existing Git status tests |
| 6 | Selected commits use literal paths and `--only`; already-staged deletions work; failed commits restore index and report restoration errors. | Unrelated staging, selected add/delete, failing hook tests |
| 7 | Static serving uses descriptor-relative no-follow opens and filtered listings. | Hidden-file symlink regression and static restart HTTP checks |
| 8 | Workspace sync errors preserve link and data; remote uncertainty no longer triggers destruction. | Revision-conflict regression; existing source-sync/replacement tests |
| 9 | Editor revision preconditions, request generations, draft preservation and encoded paths prevent stale-selection writes. | Five Chromium editor tests plus delayed-tree test |
| 10 | Regular-file modes survive replacements; imports/forks/source transfers preserve executable modes. | Executable-edit and ZIP round-trip checks |
| 11 | Import FK migration adds cascade; test DB enables production FK settings. | Imported-repo deletion and database-abort recovery tests |
| 12 | Strict diff parsing validates ranges/counts/EOF, accepts quoted/spaced paths, rejects unsupported operations, gates fuzz and retains preview expectations. | EOF/insertion regressions, strict fixtures, actual Git-produced diffs, stale preview test |
| 13 | Source apply orders removals deepest-first and supports directory/file transitions without deleting unmanaged children. | Both transition directions and failure rollback |
| 14 | Dependency plans/hash inputs cover nested manifests, workspace files, Python and mixed projects; env validated before side effects. | Nested dependency hash tests and invalid-env provisioning test |
| 15 | Paged reads flush pending EOF fragments correctly. | Exact 65,536-byte final-line regression |
| 16 | Cancellable generation-specific readiness has a deadline; Stop remains available during startup/install. | Real six-second startup, deadline kill, dependency-install cancellation, Chromium Stop test |
| 17 | Immediate port reservations, scoped/persisted ingress ownership, shared cleanup and returned preview URLs. | Concurrent reservation test; cleanup failure/config-change recovery test |
| 18 | Delete cancels/stops runtime resources before quarantine/DB deletion. Orphan PIDs retain recovery records and cannot be blindly killed; remote failures retain handles. | Database-trigger failure recovery; runtime lifecycle checks |
| 19 | Caller contexts reach local commands/Git; per-repo admission precedes global capacity; remote polling errors cancel or explicitly report uncertain operation IDs. | Cancellation, starvation and failed-poll/cancel tests |
| 20 | Issues/history have offset pagination/totals and deterministic tie-breaking; deep links load directly. | 205-issue DB test; 201-comment test; Chromium pagination/deep-link tests |
| 21 | Real static/Go/Python starters, explicit missing/ambiguous template errors, scoped forks and source filtering; atomic metadata updates and authenticated actors. | Template, metadata-validation/concurrent-update and manifest integration tests |
| 22 | File/repo/import/export/clone budgets, streaming grep/export, generated-tree pruning and bounded inline MCP export with authenticated download fallback. | Size/batch-limit tests, archive tests and memory benchmarks |
| 23 | Revision-keyed page/summary caches, archive-free workspace previews, debounced tree refreshes, slower card polling and bounded large-file rendering. | Same-size/mtime-preserving rewrite invalidation, summary invalidation and benchmarks |
| 24 | Unique run logs, byte-offset SSE resume/reset, bounded browser tail and process-log tail retention; bounded log-file history. | SSE resume, final-failure tail and repeated restart retention tests |
| 25 | Workspace execution is default; local execution requires explicit installation trust configuration. | Existing command tests plus documented/runtime-enforced trust gate |
| 26 | Single embedded manifest, shared mutation/metadata/lifecycle helpers, extracted viewer/dev UI, current README, reproducible TS/browser scripts and production-like DB fixtures. | Manifest agreement, strict TypeScript, bundles and behavior suites |

## Compatibility and release considerations

- Local commands/dev scripts now require `trusted_local_execution=true`; finite
  commands default to the optional Workspaces integration. This is intentional
  and must be accounted for when upgrading existing installations.
- Oversized MCP exports return an authenticated download alternative instead of
  unbounded base64. Consumers must support that documented response form.
- Strict patches reject formerly tolerated malformed/unsupported diffs. Fuzzy
  application is explicit and reported. Preview IDs are process-local and expire.
- Existing globally exposed dev hostnames change to include scoped identity.
  New ownership is durable. Migration cannot infer unknown historical ingress
  created by older code; check old public routes during an actual upgrade.
- Rollback covers reported failures, not sudden process/power loss. Long-running
  trusted dev tools can bypass Code locks; Workspaces supplies the isolated
  source-transfer boundary.
- Readiness currently checks TCP availability. App-specific health checks and
  automatic adoption of orphan processes require a stronger runtime identity
  contract; unverified processes are retained safely for operator recovery.
- Cross-app tests use platform test doubles. Actual Workspaces/Containers and
  Simulator deployment/scenario runs, real provider credentials, public ingress,
  and a production upgrade were not exercised. Publishing the release does not
  upgrade installed running instances.

## Verification results

- **192 Go tests/subtests passed (163 top-level)** in the final combined
  `go test -race -tags integration ./... -timeout 180s` run; no failed tests.
  This includes the original 16 audit regressions, actual sidecar HTTP/MCP
  integration, real child-process startup/cancellation, and a disposable
  smart-HTTP Git server for clone/fetch/pull/branch/commit/push.
- **9 Chromium behavior tests passed**, exercising the actual React panel with
  deliberately delayed/reordered REST responses.
- **4 Bun link/event tests passed**; strict TypeScript checking passed.
- **4 production Code panels rebuilt**; the shared build script's host import
  verification passed for all 140 panels in this checkout.
- `go vet ./...`, Linux amd64 cross-compilation, frozen Bun dependency install,
  and `git diff --check` passed.


Benchmark fixtures compare the original 0.8.2 code with this implementation on
Darwin arm64 (Apple M1 Pro): a 245 KB paged file, an existing-file replacement
in a 3,000-file tree, and a 390 KB / 10,000-line no-match grep. Three alternating
runs per version reduce timing noise; results are microbenchmarks rather than
production load tests.


| Benchmark | Original median | Fixed median | Original allocations | Fixed allocations |
| --- | --- | --- | --- | --- |
| Repeated paged read | 566.0 μs | 106.6 μs | 131.7 KiB/op | 57.6 KiB/op |
| Same-size write, 3,000 files | 441.1 μs | 391.6 μs | 12.1 KiB/op | 12.4 KiB/op |
| Streaming grep, no matches | 1235.9 μs | 686.8 μs | 956.9 KiB/op | 80.7 KiB/op |

Repeated page reads were **5.3× faster** in this sample; streaming grep used
**about 92% fewer allocated bytes**. These figures describe the fixtures above,
not guaranteed production latency.
