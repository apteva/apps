> New in 1.11.3: protocol buffering now has its own soft/strict mode, target, hard ceiling and bounded wait settings, independent of worker memory mode. See [protocol settings and validation](SOFT_PROTOCOL.md). The 1.11.1 protocol behavior documented below describes the prior released defaults.

# Capacity, memory and deadlines

These Functions changes ship in 1.10.0. The companion server cancellation patch requires a separate platform release; Functions alone cannot cancel provider work inside an older gateway. See [CAPACITY_VALIDATION.md](CAPACITY_VALIDATION.md) for test evidence and rollout notes.

## Read usage through HTTP or MCP

Use the normal authenticated app proxy, with the installation and project you want to inspect:

```sh
curl -H "Authorization: Bearer $APTEVA_API_KEY" \
  "$APTEVA_URL/api/apps/functions/capacity?install_id=10&project_id=YOUR_PROJECT"

curl -H "Authorization: Bearer $APTEVA_API_KEY" \
  "$APTEVA_URL/api/apps/functions/invocations/21173?install_id=10&project_id=YOUR_PROJECT"
```

The example invocation ID illustrates the endpoint; old invocations have no retroactive memory measurements. Direct sidecar paths are `/capacity` and `/invocations/:id`. MCP `functions_capacity` returns the same project-scoped live capacity snapshot. Existing `functions_invocations`, HTTP invocation lists and invocation detail include `resources`. `functions_invoke` includes resources in its result.

`GET /capacity` returns:

- `settings`: effective global limits and protected allocations.
- `effective_host_memory_mb` and `validation_warning`.
- `global`: reserved memory by class, live/starting workers, measured worker memory, measurement completeness, downstream occupancy, queued calls and protocol reservations by class, and rejection counts since process startup.
- `functions`: per-function worker counts, active/idle counts, reservations, actual measured memory and queued calls.
- `workers`: function/version identity, PID, class, running/waiting/idle state, invocation ID, allowance, current measured memory, measurement source and OOM count.
- `calls`: live invocation resource records with parent IDs, state, sampled memory and downstream calls.
- `queue_depth`: waiting calls in the requested project. Running calls are not counted as queued.

Function, worker and call details are project-scoped. Global totals describe the shared installation budget. A project-scoped installation always uses its own project, regardless of a query parameter.

A completed invocation's `resources` object includes:

```json
{
  "invocation_id": 42,
  "function_id": 7,
  "function_name": "evaluate",
  "project_id": "YOUR_PROJECT",
  "class": "background",
  "state": "ok",
  "worker_allowance_mb": 256,
  "worker_memory_start_bytes": 3973120,
  "worker_memory_current_bytes": 54091776,
  "worker_memory_sampled_peak_bytes": 54091776,
  "memory_source": "cgroup_v2",
  "capacity_wait_ms": 0,
  "downstream_buffer_reserved_bytes": 0,
  "downstream_buffer_peak_reserved_bytes": 8388608,
  "downstream_calls": []
}
```

These example memory readings came from a local Linux allocation test. Worker measurements include the runtime, native memory, loaded code and allocations retained from previous calls. They are **not** allocations exclusively attributable to the handler. Peaks are sampled every 50 ms, plus call boundaries; shorter spikes can be missed. Cgroup readings include descendants; the Linux RSS fallback explicitly identifies that it excludes descendants. Unsupported platforms/missing readings return null with `memory_source: unavailable`, never an invented zero. Downstream buffer fields are reservations in the sidecar, not measured heap usage. Worker totals exclude the sidecar, build processes and other apps.

Downstream records include target, kind, call ID, duration, effective timeout and error code. At most 128 records are kept per invocation; excess records are counted in `downstream_records_dropped`. Parent/child invocations have separate measurements; do not add a parent worker's peak to its children's peaks and call the result a simultaneous peak.

The small `function_invocation_resources` side table retains completed records alongside existing invocation history. Migration 006 creates three small tables; it does not rebuild or add an index to the large invocation table. Historical records remain null. No transcript, provider prompt or response payload is copied into resource telemetry.

## Configure a function

PATCH the existing `/functions/:id` endpoint (or use `functions_update`):

```json
{
  "max_memory_mb": 256,
  "timeout_ms": 300000,
  "limits": {
    "class": "background",
    "concurrency": 4,
    "max_idle_workers": 1,
    "idle_timeout_ms": 60000,
    "queue_timeout_ms": 10000,
    "app_timeout_ms": 30000,
    "integration_timeout_ms": 300000
  }
}
```

`limits` replaces the policy object. Omitted values inherit defaults: class interactive, concurrency 8, idle lifetime 300000 ms, capacity wait 10000 ms, and operator callback deadlines. Omitted `max_idle_workers` allows up to the worker-pool ceiling; explicit zero disables idle retention. Durations are bounded at 600000 ms. Invocation timeout remains capped at 300000 ms and always wins if shorter. Mark AI job functions as background explicitly; classification is controlled by function configuration, not event payloads.

The current default memory allowance remains 256 MiB, with a default operator maximum of 1024 MiB. The operator can raise that ceiling up to the validated safety ceiling of 65536 MiB; each cold start must also fit its class and total admission budgets; soft mode uses measured usage for existing workers. Node old-space receives a smaller heap hint to leave native memory headroom; cgroups remain the hard Linux memory boundary.

## Configure the installation

`GET /capacity/settings` reads settings; `PUT /capacity/settings` replaces the full object returned by GET. The panel provides labeled controls for the same API. Normal authenticated app-management access is required. Persisted settings survive restart and take precedence over environment-derived defaults.

Defaults with the existing 4096 MiB / 32-worker configuration:

| Setting | Default |
|---|---:|
| Worker memory admission mode | soft |
| Total worker memory target | 4096 MiB |
| Maximum workers | 32 |
| Interactive protected memory / workers | 1024 MiB / 8 |
| Nested-only memory / workers | 512 MiB / 4 |
| Maximum downstream calls | 64 |
| Interactive protected / nested-only downstream slots | 16 / 8 |
| Global / per-function waiting queue | 256 / 64 |
| Protected interactive / nested-only queue slots | 64 / 32 |
| Background preparation worker budget | 2 |
| Host headroom | 512 MiB |
| Maximum nested depth | 4 |
| App / integration timeout | 30000 / 300000 ms |

Reservations stay within the total; they are not additional capacity. Background work cannot consume protected interactive or child allocations, including their queue slots. Interactive work may use shared capacity but cannot consume the child reserve. Background preparation uses the background/shared allocation and a small additional concurrency ceiling. Idle workers are evicted before waiting; background admission does not evict interactive or child workers. No active invocation is killed to make space.

Downstream response-buffer reservations protect interactive and nested work: background calls can reserve up to 5/8 of the protocol memory ceiling and roots collectively up to 7/8. As of 1.11.1, the remaining 1/8 is a **protected nested allowance, not a cap**: children may borrow unused shared capacity up to the hard global byte limit. The default remains 128 MiB, including frame allocations and callback response allowances. Each downstream call still reserves up to 8 MiB. Nested calls fail promptly only when their request cannot fit the global budget, rather than waiting behind parents that may hold it.

`protocol_capacity` in GET `/capacity` and MCP `functions_capacity` exposes `hard_limit_bytes`, `reserved_bytes`, `available_bytes`, `nested_protected_bytes`, `nested_reserved_bytes`, `nested_borrowed_bytes`, `nested_can_borrow_shared`, `root_reservation_limit_bytes` and `background_reservation_limit_bytes`. The panel shows protected and borrowed nested buffers. Existing `protocol_reserved_bytes`, `global.protocol_reserved_by_class` and per-call buffer fields keep their meanings. This is independent of soft worker-memory accounting; neither the hard total buffer limit nor the 8 MiB frame-size limit is relaxed. Genuine global exhaustion still returns `protocol_memory_limit` with a global-budget reason.

Queue waits share one capacity deadline across per-function admission and memory admission, also bounded by the invocation deadline. Queue size checks and memory/worker reservations are atomic. Cancellation removes waiting requests. When limits remain occupied, the request receives a typed reason rather than waiting indefinitely. This is bounded priority admission, not a guarantee that every request succeeds under overload.

Host validation checks physical memory and enclosing Linux cgroup limits, with room for configured build processes, protocol buffers and host headroom. It cannot infer a safe allowance for unrelated services; set adequate host headroom for the deployment. Unsupported host measurements are reported as unavailable. For compatibility, an existing environment configuration starts with a visible warning when it fails the new validation; API changes must pass validation. In strict mode, lowering total limits below current allowances returns 409 and requires draining first. Lowering class limits affects new admission and does not kill existing work.

## Soft memory admission (default)

`settings.memory_mode` is `soft` by default, including existing persisted settings that predate this field. Set `APTEVA_FUNCTIONS_MEMORY_MODE=strict` for the startup default, or save `memory_mode: "strict"` through `PUT /capacity/settings`. Persisted explicit settings take precedence over the environment. The PUT endpoint accepts the complete settings object from GET with the desired fields changed; an older client omitting `memory_mode` selects soft.

Soft mode separates a worker's hard `max_memory_mb` from its scheduling charge. For a measured Linux cgroup, charge current usage rounded up to MiB plus 25% (at least 16 MiB), capped at its hard allowance, but never below measured usage. A 256 MiB worker using 40 MiB counts as 56 MiB. Measurements are cached for at most 100 ms. Starting workers retain their **full allowance** until registered and measured. Missing cgroup measurements, including RSS-only readings and unsupported platforms, also retain the full allowance. This avoids treating unknown usage as zero.

The total and class memory budgets apply to these scheduling charges in soft mode. Combined hard worker limits may exceed the target. This is controlled overcommit, not an aggregate kernel memory cap or a guarantee against simultaneous spikes. Per-worker cgroup memory limits, worker counts, queue bounds and protected interactive/nested partitions remain enforced. New cold starts also check physical `MemAvailable` and remaining enclosing cgroup memory against host headroom and pending starts. Builds remain separately bounded. Existing active calls are not killed to make room.

Under real pressure, eligible idle workers are evicted before bounded waiting. Memory is rechecked during waits, so admission can recover when a live process frees memory without exiting. `memory_pressure` identifies scheduling-target pressure; `host_memory_pressure` identifies insufficient host/container headroom. Cancellation releases waiting slots. Nested calls still fail promptly if their protected capacity is occupied, avoiding a parent/child deadlock. Ordinary warm-worker reuse remains available; this admission check controls new workers, not every allocation by a running handler.

The capacity API includes:

- `memory_admission.mode`, `accounted_memory_mb`, `accounted_by_class_mb`, `starting_allowance_mb`, `fallback_workers`, `host_available_memory_mb` (null when unavailable), and `sample_max_age_ms`.
- `workers[].admission_memory_mb` and `functions[].admission_memory_mb` beside their measured memory and hard allowances.
- Existing `reserved_memory_mb` fields retain their original meaning: the sum of configured worker limits. They are **not** actual memory or the admission charge in soft mode.

The themed panel offers the soft/strict selector and displays admission usage, combined hard limits and actual memory separately. Per-call measurements retain their existing semantics.

Switching to strict with live allowances above the total returns 409 until drained. Soft targets may be lowered while work is running; new starts wait for enough room without killing existing calls. Host validation still applies to settings changes. No database migration is required.

## Deadlines, nested calls and errors

Integrations use a separate pooled HTTP transport from ordinary app calls. Effective downstream time is the shorter of the configured operation timeout and remaining invocation time, including admission waits, headers and body reading. Dial and TLS handshake timeouts remain 10 seconds. Connection metadata lookups retain a 30-second bound. Changing Functions does not override a provider/catalog tool's own stricter timeout.

Direct `context.call("functions", "functions_invoke", ...)` calls execute with trusted in-process ancestry, the parent's cancellation/deadline and the child's policy. Parents retain their memory accounting while waiting; soft mode measures the live worker and strict mode counts its hard allowance. Children use the finite nested reserve; insufficient child capacity, excessive depth or recursion returns an explicit error immediately. This avoids waiting behind parents that hold all root capacity. Calls routed indirectly through unrelated apps do not acquire synthetic trusted ancestry; use direct Functions calls for bounded nested workflows.

Relevant `resources.error_code` / HTTP error codes include:

- `integration_timeout`, `app_call_timeout`, `invocation_timeout`, `upstream_timeout`, `caller_canceled`.
- `memory_pressure`, `host_memory_pressure`, `memory_budget_exhausted`, `worker_memory_limit`, `worker_limit`, `function_worker_limit`.
- `queue_limit`, `function_queue_limit`, `protocol_memory_limit`, `nested_capacity_exhausted`, `nested_downstream_limit`, `nested_cycle`, `nested_depth_limit`.
- `worker_oom`, based on cgroup OOM evidence, not merely an arbitrary killed process.

Capacity errors include requested/available memory where applicable and whether retrying could help. Completed invocation records remain accessible after errors. Handlers may catch downstream failures intentionally; their downstream resource records still retain the error cause. Functions does not reinterpret a handler's `{failed: 1}` response as a thrown business error.

The accompanying Apteva server changes propagate callback cancellation through provider HTTP, rate-limit waits, credential-refresh waits and delegated provider requests. Provider response-body read errors are no longer ignored. Deploy both components to get end-to-end provider cancellation. Legacy custom SDK implementations without context-aware methods cannot forcibly interrupt their own code; they retain their downstream reservation until they return, with bounded invocation cleanup. The production HTTP adapter is cancellation-aware.
