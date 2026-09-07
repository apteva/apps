# Capacity and deadline validation

Functions 1.10.0 release validation, performed locally on 2026-09-07. Publishing the release does not upgrade existing installations. The companion server changes are prepared separately. No production AI requests, transcripts or recordings were replayed.

## Behavior and evidence

| Scenario | Result |
|---|---|
| Mock integration waits 35 and 60 seconds before response headers | Both complete successfully under a 90-second invocation deadline; the former transport would abort headers at 30 seconds. |
| Integration timeout and caller cancellation | Distinct codes; repeated failures release downstream slots and workers. |
| Ordinary app deadline vs invocation deadline | Distinct `app_call_timeout` and `invocation_timeout`; short app protection remains. |
| Two 256 MiB background evaluation workers and a 128 MiB session function | Session invocation completed in **1.73 ms** while the background mocks were still waiting. This is a local synthetic latency measurement, not a production benchmark or a percentage speedup. |
| Concurrent mixed reservations | 24 requests using 128/256 MiB allowances never exceed the configured root budget and release all reservations. |
| Queued cancellation and priority | Canceled requests release global/per-function slots; interactive queue, downstream and protocol capacity remain protected from background work. |
| Nested functions | Child calls preserve ancestry/cancellation, use bounded child capacity, and reject cycles/depth or exhausted child capacity explicitly. |
| Idle eviction / restart / relocation / missing artifacts | Existing lifecycle tests pass; a ready function's first invocation does not compile. Warm preparation waits follow a build-only job that was upgraded concurrently. |
| Capacity and invocation APIs | Policy/settings validation, atomic updates, live/stored resources and project isolation exercised. |
| Linux memory accounting | 256 MiB allowance; measured worker start **3,760,128 bytes**, sampled peak **54,194,176 bytes** (about **51.7 MiB**) while touching a 48 MiB allocation. |
| Actual Linux OOM | A separate worker with a 64 MiB limit attempts 256 MiB; cgroup evidence yields `worker_oom`, and capacity is released. |
| Legacy client ignoring cancellation | Its downstream slot stays reserved until its call returns; the timed-out worker is not reused. |
| Custom callback panic | Contained as a downstream error and releases its slot. |
| Gateway propagation | Provider HTTP cancellation, response-body deadline and credential/provider tests pass, including race checks. |

The old 30-second response-header cap was removed from both pooled callback transports. Ordinary app and integration timeouts now use separate, configurable contexts, always bounded by the invocation deadline. This improves completion of long AI operations; it does not make provider computation itself faster.

The memory numbers are measured **worker usage**, including runtime and retained allocations. A per-call peak is sampled, not a precise count of exclusive handler allocations. Linux cgroup data includes descendants; RSS fallback and unavailable data are identified explicitly.

## Checks

- Full Functions race suite, including real 35/60-second mocks: passed in 104.654 seconds before the final defensive callback-cleanup refinement.
- After that refinement: full short race suite passed in 40.150 seconds, plus the separate real 35/60-second regression. Logs are saved alongside this report.
- Preparation restart/relocation/missing-artifact and queued-cancellation regressions: five race-enabled repetitions passed.
- Apteva server `go test -short ./...`: passed (main package 69.441 seconds), plus focused integration/credential/delegated/OAuth race checks.
- `go vet ./...` and `git diff --check`: passed for Functions; server vet passed.
- Linux arm64 test binary in a local container with a 1 GiB outer limit, 2 CPUs, 128 PIDs and networking disabled: memory/OOM test passed in 7.28 seconds. Privileged access was used only for writable cgroup delegation; Functions' Landlock/seccomp and worker cgroup limits remained active.
- Functions panel: strict TypeScript check and production Bun bundle passed. Inspected with Apteva's compiled stylesheet in clean light and dark modes. Capacity expansion, call details and settings editing were exercised with synthetic API data. The panel uses host theme tokens; there is no separate theme stylesheet.

An initial Linux rerun accidentally used an amd64 test binary on the arm64 Docker VM; emulation did not implement Landlock. The native arm64 rerun above passed without bypassing the sandbox. An added legacy-client test initially expected a detached call to return before the existing pending-call drain; corrected to assert its bounded invocation timeout and resource release.

## Review and rollout

The Functions changes live on `feat/functions-capacity-api`. The accompanying server changes are in the existing server checkout. Other work was already present there, so `server-focused.patch` contains only this task's changes relative to saved pre-task files; it must not be treated as a patch against a clean upstream checkout without those existing changes.

Deploy both Functions and the server changes for end-to-end provider cancellation. Mark AI job functions as background and choose validated protected allocations; existing functions default to interactive. Provider/catalog deadlines may still be stricter. Direct Functions-to-Functions calls have trusted ancestry; indirect calls through unrelated apps do not.

See [CAPACITY_API.md](CAPACITY_API.md) for settings and API payloads. Raw verification logs and the focused server patch are saved in `/Users/marcoschwartz/Documents/code/audit-reports/functions-capacity/`.

## Release candidate verification

The final 1.10.0 manifest was tested with `GOWORK=off go test -race -count=1 -timeout 300s ./...` against published SDK v0.76.0: `ok   github.com/apteva/apps/mcp/functions 112.707s`. Darwin arm64 and Linux amd64/arm64 binaries were built, and module metadata confirms the embedded SDK version.
