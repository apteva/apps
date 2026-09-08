# Automatic admission (unreleased)

Functions now regulates execution and managed downstream calls automatically. No
function-name lists, Tables-specific rules, or user-defined concurrency groups
are required. This is admission control on the available host, not AWS Lambda's
distributed capacity provisioning. Both this Functions change and the companion
server change are needed for destination control across different caller apps.

## What happens to concurrent calls

An unknown function/version starts with one active execution. A second call waits
before checking out a worker. On a one-CPU host, CPU-heavy work stays serialized;
on a larger healthy host, measured headroom permits gradual concurrency growth.
The existing per-function concurrency setting remains an upper bound.

CPU is measured separately from elapsed time. Linux worker cgroup counters are
preferred; live process/thread/child CPU is the fallback. In-flight sampling can
recognize a waiting worker before its first long response completes. Remote
integration I/O is tracked separately. High CPU, CPU throttling, low available
memory, downstream overload, or sustained service slowdown reduce admission.
Existing executions are not killed merely because the adaptive limit decreases.

Short operations get protected headroom after four successful observations.
Authenticated Jobs/worker callers receive background priority, with age-based
queue selection to avoid a fixed permanent preference. User event data cannot
claim priority. New, unmeasured lightweight functions can still wait behind work;
the scheduler cannot know a new handler's cost before observing it.

Waiting defaults to the **existing invocation deadline**. An explicitly configured
function queue timeout can shorten that wait; an inherited/zero queue timeout
uses the function invocation budget. Managed downstream admission follows its
existing app/integration request deadline. Requests without any deadline have a
finite ten-minute admission safety ceiling. Queueing does not extend the invocation
deadline. Cancellation removes the waiter. An expired/full queue returns HTTP
429 with `error_code`, `retryable: true`, `retry_after_ms: 1000`, and
`Retry-After: 1`. This is a retry hint, not automatic handler replay.

The controller has bounded active work (8 per effective CPU, minimum 4, maximum
64), 256 total waiters, up to 64 per destination/caller, and bounded policy
tracking. Existing worker, memory, protocol-buffer and downstream hard limits
still apply. No business results are cached or coalesced, so requests with
different centres, permissions or calculation options remain separate.

Nested function work has a finite escape reserve and never waits for adaptive
admission behind its parent. If safe capacity is unavailable it returns
`adaptive_nested_overload` promptly. This prevents a waiting deadlock; it does
not promise unlimited nested depth or fan-out. The server also rejects cyclic
active app dependencies and releases dependency edges on completion/cancellation.

## API and panel

The existing authenticated Functions `/capacity` endpoint and
`functions_capacity` MCP tool now include `automatic_admission`:

- `mode`, `active`, `queued`, estimated active CPU, admission/rejection/cancellation
  counters and `last_decision`;
- `pressure`: effective CPUs, busy fraction, throttling and memory pressure;
- project-filtered `operations`: function identity, version/config identity,
  active/queued counts, learned execution and destination limits, CPU estimate
  and completed sample count;
- `downstream`: aggregate automatic downstream admission state.

Existing invocation results/detail resources add `automatic_wait_ms` and
`worker_cpu_seconds`. Each downstream record adds `admission_wait_ms` and
`service_ms`. Build, queue, worker start and execution timing remain separate;
worker capacity waiting is now attributed to queue time instead of cold-start
time. Existing sampled memory, reservations, OOM and cancellation fields remain.
Unavailable CPU is null, not a synthetic zero.

The Functions panel uses its existing Apteva components and theme classes for
automatic capacity, per-function concurrency and per-call timing/CPU displays.
For canonical destination details across caller apps, platform administrators
can use the companion server's authenticated `GET /api/admission` endpoint.
Function-level resource details stay project-scoped; host totals are shared.

## Coordination and practical limits

Controllers share active destination leases between local overlapping processes
using file locks in the stable artifact directory. Locks release on normal
completion and process exit. Do not remove live lease files. Learned estimates
and aggregate CPU accounting remain process-local, and limits reset after a
restart; this is not a distributed multi-host scheduler. Separate directories
do not coordinate. Missing telemetry keeps admission conservative, particularly
on non-Linux development hosts.

Managed SDK app/integration calls are covered. Arbitrary network calls made by
user code bypass destination admission, though their function execution remains
regulated. Long-lived protocol upgrades and management routes are excluded from
the server's ordinary request admission. Request cancellation cannot undo work
that an external provider has already committed.

## Validation

`automatic_test.go` runs actual function workers: two concurrent CPU-heavy calls
have non-overlapping handler intervals; six distinct centre calculations
(including all-centres) stay separate and complete while twelve learned-light
calls remain responsive. It also checks the HTTP overload contract, queued
cancellation and API project isolation.

`internal/admission` tests cover hardware-dependent concurrency, CPU versus I/O,
in-flight adaptation, shared destinations across distinct operations/callers,
queue limits/deadlines, pressure recovery, nested reserve, repeated failures,
shutdown, missing telemetry, shared leases and process-crash cleanup. Its Linux
test runs under a real one-CPU cgroup and verifies quota detection and heavy
execution serialization. Server tests exercise distinct HTTP request bodies,
shared target limits, cancellation, dependency cycles, response-body lifetime,
and encoded-request memory bounds.

The generic package is intentionally mirrored from server `internal/admission`:
apps cannot import server internals and must build from their published source.
Keep both copies and their tests identical when changing controller policy.
No database migration or production traffic replay is required by this change.
