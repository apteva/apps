# Configurable protocol-buffer admission in Functions 1.11.3

Worker `memory_mode` and protocol `protocol_memory_mode` are independent.
Availability can inherit soft worker memory and still encounter a hard
protocol-buffer ceiling. This change addresses the protocol budget itself.

## Settings

Use GET `/capacity/settings`, modify the returned `settings` object, and PUT
that full object back to `/capacity/settings`. The themed Capacity panel
exposes the same settings. Changes are persisted and survive restarts.

| Field | Default | Meaning |
| --- | --- | --- |
| `protocol_memory_mode` | `soft` | `soft` permits bounded bursts with measured host headroom; `strict` enforces the target. |
| `protocol_target_mb` | 128 | Normal protocol reservation target, in MiB. |
| `protocol_hard_limit_mb` | 256 | Absolute reservation ceiling in soft mode. |
| `protocol_wait_timeout_ms` | 1000 | Maximum wait for ordinary downstream buffer admission, also bounded by the call/invocation deadline. |

Example fields to update within the existing settings object:

```json
{
  "protocol_memory_mode": "soft",
  "protocol_target_mb": 128,
  "protocol_hard_limit_mb": 256,
  "protocol_wait_timeout_ms": 1000
}
```

Supported limits are 16–1024 MiB, target <= ceiling; waits are 1–30000 ms.
Settings validation includes protocol, build and worker budgets plus host
headroom against physical/enclosing-cgroup limits. Lowering the effective
ceiling below outstanding reservations returns HTTP 409. Admission and limit
changes are synchronized; lowering a limit does not kill existing work.
Older API clients that omit the new fields preserve their current values.

Environment defaults:

- `APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MODE`
- `APTEVA_FUNCTIONS_PROTOCOL_TARGET_MB`
- `APTEVA_FUNCTIONS_PROTOCOL_HARD_LIMIT_MB`
- `APTEVA_FUNCTIONS_PROTOCOL_WAIT_TIMEOUT_MS`

An explicitly set legacy `APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB` remains a hard
ceiling unless overridden by the new hard-limit setting. For example, an
explicit legacy 128 MiB ceiling does not silently become 256 MiB. Persisted
API settings override environment defaults. Without an explicit legacy limit
or persisted protocol settings, the defaults are soft / 128 / 256 / 1000.

## Behavior and safety

Each pending downstream call still reserves an 8 MiB response allowance;
this is reserved capacity, not measured allocated memory. Wire frames charge
the same shared counter by their frame size. Neither accounting nor the
8 MiB frame-size limit is removed.

Soft mode admits reservations above the target only when Linux host/cgroup
available-memory measurements leave the configured `host_headroom_mb` plus
room for outstanding burst reservations. Measurements are cached for at most
100 ms. Missing measurements (including local macOS development) deny bursts;
the ordinary target remains usable. A separate hard ceiling always applies.
External processes can still change host memory between measurements; soft
mode is not a guarantee against host-wide OOM.

Interactive/nested capacity remains protected against background reservations.
Class target fractions are unchanged (background 5/8, roots 7/8, nested may
use shared capacity); soft bursts have corresponding bounds at the hard
ceiling and require headroom. Ordinary calls wait briefly for released bytes.
Nested calls borrow immediately when safe and fail promptly otherwise: waiting
behind a parent holding buffers could deadlock. No business handler is replayed
or retried automatically by this change.

## API diagnostics

GET `/capacity` and MCP `functions_capacity` include `protocol_capacity`:

- Mode, target, hard ceiling, reserved bytes, available bytes, over-target bytes.
- Waiting-call count and burst admissions since the current policy was loaded.
- Host available memory (null if unavailable), required headroom and wait timeout.
- Frame-size and per-callback allowance bytes, protected shares and nested use.

Existing per-call `downstream_buffer_reserved_bytes` and
`downstream_buffer_peak_reserved_bytes` identify calls holding allowances.
These remain reservation metrics, not actual allocation measurements.

Errors distinguish `protocol_memory_limit` (global/class ceiling or wait
exhaustion) from `protocol_host_memory_pressure` (burst prevented by low or
unavailable host headroom). Caller cancellation and parent timeouts keep their
own classifications. Hard-ceiling failures remain possible by design.

## Local proof

The same admission path was exercised with 24 simultaneous pending HTTP mock
callbacks. The mock memory reader supplied sufficient headroom for a
repeatable comparison; no production AI integrations or business data were used.

| Protocol policy | Simultaneous admitted callbacks | Rejections |
| --- | ---: | ---: |
| Strict, 128 MiB target | 16 | 8 |
| Soft, 128 MiB target / 256 MiB ceiling | 24 | 0 |

Additional coverage: concurrent attempts at the hard ceiling, combined frame
and callback accounting, low/unavailable host memory, short pressure followed
by successful admission, bounded waiting, cancellation and reservation cleanup,
settings validation/persistence, and legacy environment compatibility. The
full Functions race suite, Linux container tests, vet, panel build and
TypeScript checks are included in local validation.

Publishing this release does not upgrade installations or change production
settings. This feature requires no Apteva server change. The separate sale-owner
issue is outside this change.
