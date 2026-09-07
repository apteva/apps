# Fleet deployment timeout and Functions build diagnostics

Verified on 2026-09-07 against Fleet 0.10.8 and Functions 1.10.0 source. Fixes ship in Fleet **0.10.9** and Functions **1.10.1**, preserving their previous release history. Publishing these versions does not upgrade an existing installation. The original production logs do not conclusively identify the cause of `signal: killed`.

## Findings

- Fleet used the shared 10-second HTTP client in `tenantAppRPC`, with `context.Background()`. Synchronous Functions creation/deployment could therefore be canceled long before Functions' two-minute build budget. Cancellation of the tenant HTTP request reaches Functions, whose compiler cancellation kills the process group. This is a plausible mechanism for the reported failure, not a retrospective proof.
- Go's telemetry mode defaults to local collection when no mode file exists. Its helper can attempt a sandbox-blocked `setsid`; the warning is non-fatal. Merely setting `GOTELEMETRY=off` is not the configuration mechanism used by the tested Go toolchain.
- Compiler errors discarded context and disk-watchdog causes, and cleanup removed build cgroups before their counters were saved.
- Sandboxed builds default to 1,024 MiB (`APTEVA_FUNCTIONS_BUILD_MEMORY_MB`), separate from a function's 256 MiB worker allowance. Increasing the worker allowance cannot fix an outer request timeout. Trusted standard-library cache priming previously used a separate command path; it now uses the same bounded sandbox build runner and diagnostics.

## Implemented

Fleet `tenant_app_call` uses the SDK context-aware handler and propagates its incoming request context. Calls to Functions create/deploy/rollback/prepare default to **150 seconds**; ordinary app calls remain at **10 seconds**. Optional top-level `timeout_ms` accepts 1..300000, and a shorter parent deadline always wins. Headers and response bodies share that deadline. Response read failures and oversized responses are no longer silently accepted. Fleet is pinned to published SDK v0.76.0.

```json
{
  "tenant_id": "TENANT_ID",
  "app": "functions",
  "tool": "functions_deploy",
  "project_id": "PROJECT_ID",
  "timeout_ms": 150000,
  "arguments": {"id": 7, "source": "..."}
}
```

The timeout belongs to Fleet's call arguments, outside the target tool's `arguments`. This uses the deployment-specific timeout option from the report; it does not introduce an asynchronous deployment API or automatic retries. Any shorter outer agent/gateway deadline still applies.

Functions creates telemetry `off` mode files inside its isolated build home for Linux and Darwin, sets isolated `XDG_CONFIG_HOME`, and disables persisted Go environment configuration. It does not alter the host's Go telemetry settings or loosen seccomp/Landlock.

Build diagnostics are captured before cgroup cleanup. Failures retain an error that unwraps the original cancellation/command cause, and a JSON `build_diagnostics` record is included in the version's existing `build_log`. Successful commands log the same diagnostic record. Fields include elapsed milliseconds, reason, cancellation reason, configured build memory, memory peak, memory events and PID events. Missing kernel measurements are null/unavailable. An arbitrary SIGKILL is not reported as OOM without cgroup evidence. HTTP disconnection is visible as caller cancellation; Functions cannot determine which upstream component disconnected solely from that context.

## Verification

- Real Functions sidecar reached through Fleet's actual `tenant_app_call` implementation and a local tenant HTTP proxy. A fresh Go cache and an explicit 11-second compiler gate guarantee a cold deployment exceeds the former cap; deployment completed successfully in **14.74 seconds** against the final 1.10.1 release binary. The controlled delay is part of the duration, not a claim that Go spent all that time compiling. This does not replay production or emulate every production gateway timeout.
- Full race suites for Functions (109.321 seconds) and Fleet with integration tags (33.547 seconds) passed; focused tests cover ordinary/deployment timeout policy, invalid limits, caller cancellation, telemetry mode, build cancellation and disk-watchdog diagnostics.
- Native Linux arm64 checks ran with delegated cgroups, Landlock and seccomp enforced: telemetry off, cancellation, disk watchdog, cgroup counter capture and real Go worker memory/OOM tests all passed.
- A failing sandboxed build saved `memory_peak_bytes: 593920`, memory event counters including `oom_kill: 0`, and PID events including `max: 0` before cgroup deletion. These are real observed counters, not fabricated zeros for missing data.
- Both apps pass `go vet`; no production state was changed.

Reproduce the end-to-end check by building Functions with `GOWORK=off go build -o /tmp/functions-build-test .`, then in Fleet run `GOWORK=off FUNCTIONS_TEST_BINARY=/tmp/functions-build-test go test -race -run TestFleetColdGoDeploymentBeyondTenSeconds -v -timeout 180s .`. Logs are saved under `/Users/marcoschwartz/Documents/code/audit-reports/fleet-functions-builds/`.
