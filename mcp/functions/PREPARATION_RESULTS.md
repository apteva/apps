# Functions 1.9.0 preparation validation

Measured on September 6, 2026 using the final 1.9.0 source and real 1.8.1/1.9.0 binaries, both embedding app-sdk v0.76.0. These are synthetic local measurements, not measurements of Scaleway production or dashboard page load time.

## First-request comparison

The benchmark deploys a real Go handler through HTTP, stops the app, removes that function's artifact directory, and restarts. It retains the trusted standard-library cache, as a normal upgrade does. For 1.9.0 it waits for the function's `runtime_readiness.state=ready` before sending the first invocation. A separate upgrade scenario deploys with 1.8.1, preserves the old artifacts and database, and restarts with 1.9.0. Each invocation's response is checked. Preparation does not invoke the handler.

Native arm64 macOS, three runs per scenario, with scenario order alternated:

| Scenario | Run 1 first request | Run 2 | Run 3 | Median |
|---|---:|---:|---:|---:|
| 1.8.1, missing artifacts | 799.91 ms | 813.14 ms | 816.58 ms | **813.14 ms** |
| 1.9.0, missing artifacts, prepared | 1.75 ms | 1.29 ms | 1.59 ms | **1.59 ms** |
| 1.8.1 → 1.9.0 upgrade, prepared | 1.63 ms | 1.45 ms | 1.37 ms | **1.45 ms** |

The paired artifact-loss comparison reduces median first-request latency by **99.80%**. The 1.8.1 first requests recorded 614–631 ms of build time and 179–196 ms of worker startup. Every prepared 1.9.0 first request recorded zero milliseconds for request-time preparation wait and worker startup (integer-millisecond counters). Lifecycle tests independently assert zero additional artifact builds for ready first requests.

This moves work ahead of traffic; it does not eliminate compilation. Background preparation after the management API became healthy took 802–818 ms for artifact loss and 804–859 ms for upgrades. If traffic arrives before preparation completes, it can still wait. Management health does not promise all functions are ready.

Each run also sends 50 warm requests. The medians of those per-run medians were 0.558 ms for 1.8.1, 0.668 ms for prepared 1.9.0 and 0.706 ms after upgrading. There is no demonstrated warm-request speedup; the observed extra 0.11–0.15 ms includes readiness/artifact checks and local measurement noise. These small samples do not establish production throughput or tail latency, and downstream app execution speed is unchanged.

## Linux execution check

The final Linux arm64 binaries also ran the same script in `ghcr.io/apteva/workspace-dev:0.1.1`, with no network, two CPUs, a 1 GiB memory cap and a 128-process limit. One run per scenario:

| Scenario | First request | Background preparation | Warm median (50 requests) |
|---|---:|---:|---:|
| 1.8.1, missing artifacts | 352.45 ms | — | 1.544 ms |
| 1.9.0, missing artifacts, prepared | 4.94 ms | 341.67 ms | 1.685 ms |
| 1.8.1 → 1.9.0 upgrade, prepared | 2.33 ms | 360.97 ms | 1.716 ms |

The artifact-loss comparison was 98.60% lower. Both 1.9.0 first requests recorded zero request-time build/worker-start milliseconds. This is a Linux smoke measurement, not a statistical production benchmark. The container's read-only cgroup environment required `APTEVA_FUNCTIONS_REQUIRE_CGROUP=false`; outer resource limits remained enabled. Outer seccomp was unconfined to permit the function runtime's own sandbox setup; the app's inner Landlock/seccomp remained enabled. No production configuration was changed. Linux amd64 was cross-built but was not executed in this arm64 container.

## Regression and UI coverage

- Full Go suite passed (26.315 seconds); full race suite passed (36.512 seconds). Existing partial-migration recovery tests also passed. This release adds no migration; the production-sized migration fixture from the 1.8.1 release was not rerun for this change.
- Real Node worker lifecycle tests cover compatible restart, restore into another directory, missing artifacts, incompatible markers and legacy source recovery after relocation. Compatible restart/restore reuse artifacts; missing/incompatible artifacts rebuild once. Ready first requests and ordinary worker idle eviction do not rebuild.
- Sixteen simultaneous requests after artifact loss share one build. Preparation against a handler that throws succeeds without invoking that handler or inserting invocation rows.
- Build-only preparation reports `prepared`; warming reports `ready` without another build. Tests cover caller cancellation, parent deadlines and function deadlines, and verify readiness remains correct when API responses mask secret environment values.
- Tool identity tests cover identical tool contents with different locations/mtimes, changed contents, and separate Go/Node fingerprints.
- `go vet ./...`, panel JavaScript syntax checks and `git diff --check` passed. The production panel bundle was rebuilt with Bun.
- Browser review of the production panel against a local fixture verified failed → preparing → ready transitions, preparation controls, actionable errors, build/worker/invocation timings and the Upstream timeout label. This UI check used synthetic data rather than a production dashboard.

## Reproduction

Build the baseline from tag `functions/v1.8.1` and the candidate from `functions/v1.9.0`, with `GOWORK=off` so both use their pinned SDK. Run from `mcp/functions`:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
python3 scripts/compare-preparation.py \
  --baseline /path/to/functions-1.8.1 \
  --candidate /path/to/functions-1.9.0 \
  --baseline-migrations migrations --runs 3
```

The script uses temporary databases, local HTTP ports and synthetic functions, prints JSON measurements and removes its temporary data. Node, npm and Go must be available for the app's runtime checks. Build scripts and module initialization may execute during preparation; the guarantee is that the business handler is not invoked.
