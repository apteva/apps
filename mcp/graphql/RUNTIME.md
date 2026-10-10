# Reusable runtime capabilities (GraphQL 0.8.0)

GraphQL owns execution, authorization, admission, batching and read-context
lifecycle. Applications own schema fields, resolver mappings, projection SQL,
freshness requirements and fallback decisions. There are no Flexylead fields or
business rules in the runtime.

## Release configuration

The Deploy tab exposes runtime controls. The JSON editor and
`graphql_release_publish` accept the same immutable release limits:

```json
{
  "max_concurrent_requests": 128,
  "max_concurrent_operations": 32,
  "max_queued_operations": 128,
  "max_queue_ms": 1000,
  "max_execution_ms": 15000,
  "max_response_bytes": 4194304,
  "max_parallelism": 8,
  "max_coalesced_waiters": 128,
  "coalesce_reads": false,
  "read_consistency": "none",
  "max_snapshot_ms": 15000
}
```

Existing cost, depth, row and resolver budgets remain supported. Admission
bounds both total executions per API/environment and executions of each
canonical document/selected operation. Variables do not create new admission
lanes. The queued-execution budget applies to both lanes; zero disables waiting.
Queue timeouts are separate from execution deadlines. Caller deadlines and
verified credential expiry always bound execution. The public endpoint no longer
adds a conflicting fixed 15-second execution ceiling; Auth verification remains
bounded to five seconds. Queue saturation/waiter limits return HTTP 429, queue
and execution timeouts return 504, and cancellation returns 408 where a response
can still be written. These limits are per running GraphQL sidecar, not a
cluster-wide scheduler. Idle admission lanes are removed.

## Opt-in in-flight sharing

`coalesce_reads` shares eligible query executions while they are running. It
requires an immutable published release. Mutations and subscriptions cannot
join a flight. Tables read operations and deterministic published modules are
eligible. HTTP, Function and upstream sources must declare `read_only: true`;
this is an API author's explicit purity contract, not inferred from GET/query
syntax. Database and unknown source purity is not assumed.

Every caller independently passes identity, operation, variable, cost and field
permission checks before joining. The key includes project/API/environment,
release ID/checksum/configuration, canonical document preserving aliases/order,
selected operation, coerced/defaulted variables, verified issuer/user/tenant,
claims, permissions, authorization version and expiry. Platform caller grants
are included when available. These keys are hashes kept in memory, never raw
identity/variable data in logs.

Each caller receives its own response maps, errors, request ID and telemetry.
Shared errors use the caller's document locations. Cancelling one waiter does
not cancel the others; the last waiter leaving cancels upstream work. The
shared deadline is bounded by queue/execution limits and credential expiry.
Completion removes the flight before releasing waiters. Successful, failed and
partial results are **never cached for subsequent requests**. Concurrent
waiters may receive the same already-running failure.

## Batching

The existing request-local loader memoizes identical reads, including failures
within that request so non-null completion never repeats a source operation.
Compatible Tables `get` calls for the same table/projection become one indexed
`rows_search` with `id IN (...)`, in groups of at most 100. It restores each
caller's row/not-found envelope and requested projection. Predicates,
pagination, hydration and incompatible configurations are not fused.
Other independent Tables operations retain bounded transport batching.

The generic upstream adapter can provide `batch_tool` for key-based bulk reads.
The backend must implement a real bulk read; an envelope containing N individual
queries is only transport batching. Tests assert backend read counts as well as
request-local deduplication. Diagnostic `backend_reads` counts backend operations,
not inferred physical SQL statements; a generic bulk envelope conservatively
counts its logical reads. `backend_calls` includes attempted transports, including
compatibility retries. Resolver completion timings include source/loader waits
and can overlap; their sums are not wall-clock request duration.

## Consistency modes

- `none`: upstream defaults, without a new consistency guarantee.
- `batch`: a consistent snapshot for each Tables batch or generic upstream batch.
  Tables uses native `tables_batch(mode: "read_snapshot")`, including large and
  single reads. Separate batches/chunks/resolver levels may see different data.
- `request`: one reusable bounded snapshot **per upstream source** for the whole
  GraphQL execution, including nested resolver levels. A snapshot handle is
  opened once, reused, expiry-checked and closed on success, partial failure or
  cancellation. Backends own transactional guarantees and must release expired
  handles even after a process crash. Cleanup is attempted with a separate
  two-second deadline; cleanup failures become GraphQL errors.

Request consistency does not imply a distributed transaction across independent
sources. Tables 0.2.8 supports snapshot batches and same-read projection metadata,
but does not expose reusable snapshot handles across separate calls. It therefore
rejects `request` mode with `snapshot_unsupported`, rather than silently weakening
the contract. Unsupported HTTP/Function/Database snapshot requests also fail.
Local calculation modules can consume values read from a snapshot; they do not
open data transactions. Mutations retain ordinary ordered execution and do not
use read snapshots.

## Generic upstream app adapter

Configure an `upstream` source (also available in the Sources tab):

```json
{
  "app": "my-backend",
  "read_tool": "read",
  "batch_tool": "read_many",
  "snapshot_open_tool": "snapshot_open",
  "snapshot_close_tool": "snapshot_close",
  "read_only": true,
  "parameters": {}
}
```

`batch_tool` and the paired snapshot tools are optional capability declarations.
The backend must be in the installation's permitted app/integration bindings;
source configuration does not grant access. Calls use the SDK's cancellable
app transport and the authoritative project scope. Inputs always include
`_project_id`, configured `parameters`, and an execution `deadline`. For verified
users, `principal` carries issuer, subject, tenant/project/API, managed claims,
permissions and authorization version; no browser tokens or arbitrary headers
are forwarded. The backend must trust only authenticated GraphQL sibling calls
and enforce the configured principal's row access on every read/open operation.

Single reads receive `operation`, coerced `arguments`, `parent`, and, when active,
`snapshot_handle`. A response's `value` is ready for GraphQL completion:

```json
{
  "value": {"id": "42"},
  "metadata": [{"name": "projection", "version": 1, "generation": 7,
                "ready": true, "stale": false,
                "coverage_from": "2026-10-01T00:00:00Z",
                "coverage_to": "2026-11-01T00:00:00Z"}]
}
```

`read_many` receives `reads: [{id, operation, arguments, parent}, ...]` and the
same snapshot handle. Return `results: {"op0": {value, metadata}, ...}` with one
entry per ID. A missing ID or an `error: {code, message}` entry fails the affected
field; private upstream error details are replaced by stable runtime errors.

`snapshot_open` receives `expires_at`, an upper bound chosen by the runtime.
Return `{handle, expires_at}` with a nonempty opaque handle and an expiry no
later than the requested deadline. Invalid/unbounded snapshots fail and trigger
cleanup. `snapshot_close` receives `snapshot_handle`; make closure idempotent.
All metadata must describe the same read/snapshot as `value`.

## GraphQL extensions and diagnostics

Read metadata is returned in `extensions.sources` as
`{source, consistency, metadata}` entries, without adding domain schema fields.
Tables `tables_query` projection status comes from the same SQLite snapshot as
its rows (Tables 0.2.7+). The runtime whitelists generic generation/version,
readiness/staleness, change watermarks, freshness publication time and coverage
bounds. It excludes handles, row contents, private backend fields and identity.
Metadata is bounded to 64 KiB per request. Applications interpret these facts
and choose their own freshness/coverage/fallback policy.

Logs and the exported `graphql-telemetry` dashboard widget expose Queued and
Shared execution quick views, queue wait and backend-operation filters/sorting,
shared execution IDs/waiter counts, loader hits, batch sizes, snapshot acquisition,
source metadata, and resolver call/error/total/max timing tables. Resolver timing
labels aggregate by schema field, not row ID or arbitrary path, and are bounded.
Summary averages and shared-caller counts cover all matching stored requests.
Coalesced callers retain independent request logs; shared backend statistics are
repeated per waiter and should not be summed as unique execution totals.

Migration 008 adds an independent runtime JSON column and filter indexes without
changing existing data. Earlier logs have no runtime diagnostics. Request bodies,
query text, variables, credentials and snapshot handles are not logged.
Telemetry remains asynchronous and can drop under overload/storage failure.
Subscriptions remain outside HTTP request diagnostics.

See [STAGED_PIPELINES.md](STAGED_PIPELINES.md) for version 3 separate-statement snapshot stages and legacy version 2 stages,
executed through one Tables snapshot batch with intermediate data kept inside SQLite.
