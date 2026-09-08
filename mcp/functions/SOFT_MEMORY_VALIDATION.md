# Soft memory admission validation

Implemented on 2026-09-08 on top of apps main `f6c5c9f1`, retaining the Functions 1.10.1 and Fleet 0.10.9 history. Not released or deployed.

## Behavior

Memory admission defaults to soft for new installs and old persisted settings without a mode. Linux cgroup usage plus 25% (minimum 16 MiB margin, capped at the hard worker allowance) replaces full-allowance accounting for measured workers. Pending starts and workers without cgroup measurements retain their full allowance. Per-worker memory limits, worker/queue limits and interactive/nested protections remain. Physical and enclosing cgroup headroom are checked before starting workers. Strict mode is available through the API and themed panel.

Measured memory is sampled at most every 100 ms on admission, with conservative pending-start accounting. This permits combined worker maximums above the scheduling target; it does not guarantee immunity from simultaneous memory spikes. Existing warm reuse and active handlers are not suspended when crossing the target. Real pressure can still produce bounded queue/pressure failures; those are distinguished from allowance, worker and OOM errors.

## Evidence

- Native Linux arm64: four concurrent real Go handlers, each with a 256 MiB hard limit, under the same 512 MiB worker target. Strict: 2 successful, 2 memory-budget rejections. Soft: 4 successful, 0 rejections, 1,024 MiB combined allowances, 11,124,736 bytes measured, 76 MiB admission accounting. The handlers sleep for 600 ms to hold concurrent workers; this measures capacity/rejections, not AI execution speed.
- Native Linux OOM regression: a 64 MiB worker attempting a 256 MiB allocation still fails with `worker_oom`. Normal allocation telemetry captured 4,005,888 bytes at start and 53,440,512 bytes sampled peak in a 256 MiB worker.
- Native host-pressure tests cover physical memory, enclosing cgroup limits and missing bounded-group measurements; pending starts cannot bypass host headroom.
- Deterministic mixed 128/256 MiB comparison, injected 40 MiB measurements: soft admits 8 workers (1,536 MiB combined limits, 448 MiB admission), strict admits 4 (768 MiB). This is scheduling logic tested with injected measurements, distinct from the real Linux test above.
- Full Functions race suite passed in 103.887 seconds, including the 35/60-second integration regressions. Added soft-memory tests passed ten race-enabled repetitions after the final test additions. The stress-test fixture was corrected to remove workers and their reservations atomically, matching the production discard path.
- Pressure recovery without worker exit, cancellation while queued, concurrent cold starts, missing/RSS-only measurements, protected interactive/nested capacity, old settings upgrade, strict opt-in persistence, invalid mode rejection, project-scoped API fields and soft-to-strict transition validation passed.
- Go vet and Darwin arm64/Linux amd64 builds passed. Native Linux test binary built for arm64 against published SDK v0.76.0.
- Panel strict TypeScript check, Bun production bundle and host import verification passed. Local synthetic API fixture exercised soft/strict save and was visually checked in Apteva clean light and dark modes.

The Linux check used a disposable network-disabled container with 4 GiB outer memory, two CPUs and 256 PIDs. Cgroup delegation was enabled for child groups; Functions seccomp, Landlock and worker/build cgroup limits remained enforced. The local Docker engine needed recovery before this test. No production services, calls, AI recordings or databases were used.

## API

`GET /capacity` and MCP `functions_capacity` expose `memory_admission`, plus per-worker/per-function `admission_memory_mb`. Existing `reserved_memory_mb` fields keep their prior meaning (sum of hard allowances). Settings use `memory_mode: soft|strict`, configurable via `PUT /capacity/settings` or the panel; startup default override is `APTEVA_FUNCTIONS_MEMORY_MODE`. See `mcp/functions/CAPACITY_API.md` for complete semantics.
