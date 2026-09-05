# Functions 1.8.0 — audit fixes and measured results

Functions 1.8.0 incorporates the audit fixes below, compared with release `functions/v1.7.0` (`a9a0310d`). Measurements use local fixtures; they do not represent production service latency.

## Measured performance

Final paired run: the original and fixed code ran concurrently in separate processes, each with its own temporary database and real Node workers. An external client alternated requests between them, reversing order each pair. App callbacks hit a real local HTTP fixture with a fixed 2 ms service delay. Both sides used GOMAXPROCS=4 on the same Apple M1 Pro. This measures local execution/callback overhead, not improvements to real upstream services.

| Workload | 1.7.0 p50 | Fixed p50 | Lower median latency | p95 before → after |
|---|---:|---:|---:|---:|
| Warm HTTP invocation | 1.230 ms | 0.760 ms | 38.2% | 3.887 → 3.423 ms |
| One app call (2 ms downstream) | 4.424 ms | 4.061 ms | 8.2% | 8.047 → 6.634 ms |
| Eight parallel app calls | 3.996 ms | 3.651 ms | 8.6% | 5.301 → 4.603 ms |
| Eight sequential app calls | 24.732 ms | 24.280 ms | 1.8% | 32.839 → 30.152 ms |

The final paired run had zero failed requests on either side: 600 warm requests, 300 single app calls, 200 parallel-call requests and 100 sequential-call requests per version. A previous paired run also showed lower medians (40% warm, 15% single-call, 13% parallel-call), but its sequential-call p95 worsened. Host load varied; do not treat these figures as guaranteed production speedups. Repeated measurements should retain all results to show variation.

Three independent allocation runs found warm-request allocations fell from about **1,073,642 bytes to 13,429 bytes (98.7% less)**. The isolated event-reader benchmark fell from median **212,069 ns to 2,867 ns (~74× faster)**, and from ~1,062,644 to 5,746 bytes/op. The previous implementation allocated a 1 MiB request buffer even for tiny JSON events; the new reader grows with the body and retains raw JSON until the worker needs it. Warm invocations also avoid repeated metadata/source reads, filesystem marker reads and runtime PATH probing.

Go deploy + first invocation, three builds in one process:

| Build | 1.7.0 | Fixed |
|---|---:|---:|
| First, including cache preparation | 4.942 s | 5.407 s |
| Second | 5.136 s | 1.176 s |
| Third | 5.160 s | 1.097 s |

Repeat builds averaged **5.148 s → 1.136 s (78% faster, ~4.5×)**. The first build pays for a trusted standard-library cache. Untrusted builds receive independent file copies and cannot populate that shared cache. First Node invocation is also much quicker because deployment now boots a validated warm candidate; this moves boot work to deploy rather than eliminating it.

Initial unpaired tight loops reproduced 1.7.0's late cancellation-watcher bug (six spurious warm timeouts in the completed comparison runs). The fixed version had none. Preliminary runs also exposed an overly strict new memory admission setting; that was corrected to use bounded waiting and covered by a 16-call regression before the final measurements. 

## Implemented audit fixes

| Audit finding | Change | Evidence |
|---|---|---|
| 1 | Null RPC inputs normalized; malformed input rejected; callback panic recovery | audit_regression_test.go, reliability_test.go |
| 2–3 | Project and immutable-identity checks on deletion, updates and late build completion; tracked workers, closing pools, unique artifact namespaces | audit_regression_test.go; TestDelayedMutationCannotTouchReusedIDs |
| 4–5 | Linux launcher pins its OS thread, validates syscall architecture and rejects alternate x86 ABI; required Landlock scope fails closed | Linux arm64 real Go invocation; amd64 cross-build |
| 6–7 | Live worker/memory reservations include idle processes; bounded protocol payloads, downstream backpressure and disk admission/monitoring; Linux cgroups required by default | TestAuditGlobalWorkerCap; TestProtocolAllocationBudget; TestParallelCallsRespectByteBackpressure; Linux cgroup failure check |
| 8 | Resolved source snapshots persisted; matching legacy snapshots recovered; repository drift refused | TestAuditRepoVersionImmutable; TestLegacySnapshotRecovery |
| 9–10 | Syntax and boot validation before activation; deployment revision compare-and-swap; validated rollback; stale builds cannot overwrite newer activation | TestAuditInvalidJSDoesNotActivate; TestBootValidationKeepsPriorVersion; TestAuditDeployCompletionOrder; TestRollback |
| 11–12 | Worker configuration hashes invalidate stale environment/memory/access; artifact fingerprints include harness, target and toolchain binary identity | TestAuditMetadataRefresh; real Node/Go rebuilds |
| 13–14 | End-to-end invocation budget; context-aware production callback transport, stable pool ownership and retained accounting for legacy callbacks | TestColdStartBudgetAfterEviction; TestCallbackCancellation; TestLegacyDownstreamBudgetHeld; repeated race checks |
| 15–16 | Full supported unary results reach callers; only stored previews are capped; serialization and oversized-frame failures explicit | TestInvokeResponseComplete; TestAuditJSONResponseNotCorrupted; TestAuditUnserializableResultFailsPromptly |
| 17–18 | SSE terminal success/error events; failed byte streams abort; incremental bounded body reads, invalid JSON errors and HTTP 413 for oversized events | streaming_test.go; TestAuditStreamFailureVisibleToClient; TestAuditLargeRequestRejected; TestInvalidBodyAndNullCRUD |
| 19 | Bounded build queue, cancellation, interrupted-job recovery, dependency lock snapshots, trusted compiler cache, artifact retention and in-flight version leases | deployment tests; Go build benchmark; TestRetentionPreservesActiveAndInFlightVersions |
| 20–21 | Invocation records start before execution and include version/config/timings; secrets redacted in audit previews; typed matching protocol IDs; invocation-scoped logs/calls | TestAccessPolicyAndLogPreview; TestLateResultIdentityRejected; TestInvocationRedactsSecretsFromAudit |
| 22–24 | Parallel detail loading with stale-result guards; project/install dialog identity; deletion refresh; repository source and dependency preservation; settings editor; incremental cancellable output | Local browser fixture: detail, repository editor, streaming error, cancellation and deletion refresh |
| 25 | Strict metadata/URL validation; safe structured headers, binary and multiple-header responses; paginated lists, masked secrets and lazy invocation details; manifest single source; atomic lead upsert; per-function access policy | TestMetadataValidation; TestStructuredResponseBinaryAndHeaders; TestListPaginationAndSecretMasking; manifest tests; browser checks |
| Additional load-test finding | Replaced the late cancellation watcher that could poison the next warm invocation; load and repeat-invocation tests verify no spurious failures | TestNoSpuriousWarmTimeouts; paired benchmark and repeat race checks |

The SDK is pinned to v0.73.0, verified as the latest tag at the SDK HEAD when the upgrade was made. Production cancellation uses a narrow adapter to the SDK's public callback HTTP API because that SDK version does not expose context arguments for app/integration calls. Optional context-aware custom clients are supported; non-context-aware clients retain their operation budget until completion.

## Validation

- Full normal Go suite passed (33.437 s), including real Node/Go compilation, invocation, app calls and streaming.
- Full race suite passed (34.121 s). Additional final identity/retention/redaction regressions also passed with the race detector (1.991 s). No outstanding race reports.
- `go vet ./...` and `git diff --check` passed.
- Linux arm64: final-code Go handler built and invoked under Landlock/seccomp in a disposable container (7.16 s); a Go handler calling another app also passed (0.35 s). The outer container had no network, all capabilities dropped, 768 MiB memory, 128 processes and two CPUs. Its outer seccomp filter was removed to permit the inner Landlock setup; inner sandboxing remained enabled. Inner cgroups were explicitly disabled for this smoke test because the outer cgroup filesystem was not delegated.
- A separate required-cgroup check failed closed, as expected, on that read-only cgroup filesystem. Production cgroups were not disabled or changed.
- Linux arm64 and amd64 production binaries cross-built successfully. Only arm64 received a runtime Linux test; this is not a claim of exhaustive kernel/ABI penetration testing.
- Bun built the production React panel bundle; Node syntax checks passed for harness and panel.
- Browser fixture checks verified incremental output before completion, explicit SSE failure, cancellation with canceled status, preserved repository/package settings and list refresh after deletion. This was a local fixture, not a production dashboard session or full browser automation suite.

## Deployment implications and limits

1. Linux defaults now require delegated cgroup v2 and scoped Landlock support (ABI 6 / Linux 6.12+). Confirm the deployment provides these before upgrading; otherwise builds/workers fail closed.
2. Disk admission and 250 ms monitoring are implemented. Hard instantaneous protection against hostile disk writers still requires operator-configured filesystem/project quotas or bounded volumes for artifacts and temporary storage. Protocol reservation limits are payload budgets, not hard sidecar RSS caps; the container also needs an appropriate memory limit.
3. New unary/control frames have an 8 MiB limit. Larger content must stream or use storage references. SSE clients must validate the terminal event; Jobs HTTP targets should use unary handlers unless their dispatcher checks stream completion. Cancellation cannot undo an upstream side effect already accepted.
4. Invocation log attribution is strongest through `context.log` / `ctx.Log`. Native stdout/stderr is best-effort. Background work after a handler returns is unsupported. Existing installations inherit installation-level call grants unless an explicit per-function `access` policy is configured.
5. Read APIs now return masked/paginated summaries; clients needing secrets must explicitly request them. See README for exact parameters and limits.

## Reproducing the measurements

The repository includes `performance_test.go`, `audit_regression_test.go` and `reliability_test.go`. To repeat baseline measurements, create a separate checkout of `functions/v1.7.0`, copy only `performance_test.go` into its Functions package, and run with `GOWORK=off`. `TestPerformance`, `TestPerformanceServer` and `TestGoBuildPerformance` are opt-in and use temporary test data. See the fixture comments for their environment flags. Alternate client requests between both servers on the same host, record errors as well as latency, and compare multiple runs.
