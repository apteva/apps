# Read diagnostics

Tables 0.1.20 carries the diagnostics developed against `tables/v0.1.16`
forward onto 0.1.19, retaining app-sdk `v0.76.0` and modernc.org/sqlite `v1.50.0`.
It does not change read concurrency, queue limits,
SQL deadlines, WAL configuration, or dependencies.

## Collect a useful comparison

After deploying this change, temporarily set the installation config
`log_all_reads` to `true`. With the default `false`, every failed read and every
read exceeding `slow_query_ms` (250 ms by default) still produces a completion
record. Turn full logging off after the measurement window.

Functions 1.11.4 already passes the Gateway request ID in `_request_id`. Tables
0.1.20 carries that ID into the detailed read record automatically, while
retaining the existing `tables call completed` summary. For other callers, pass
an optional `request_id` argument on each Tables read. For example:

```json
{
  "sql": "SELECT COUNT(*) AS count FROM {appels} WHERE direction = ?",
  "params": ["entrant"],
  "request_id": "function-request-123"
}
```

The HTTP routes also accept `X-Request-ID`. IDs must be 1–128 ASCII characters
from letters, digits, `-_.:/`; invalid diagnostic IDs are ignored. Each call
gets a separate random `call_id`, so retries and parallel calls can be separated.
The existing Functions `_request_id` takes precedence over an explicitly supplied
`request_id`. Callers outside that flow must supply their own diagnostic ID.

Reproduce `dashboard_calls`, `dashboard_outbound_calls`, and the inbound
`appels` aggregation with identical parameters and arrival load, first alone,
then with the original parallel workload. Keep the SQL deadline unchanged.
Compare errors, p50/p95 total duration, queue time, SQL time, and throughput.
Capture the host/container CPU quota, usage and throttling alongside these logs.
Only if those measurements implicate concurrent execution should an isolated
comparison of `max_read_conns=1`, `2`, and `4` be used to select a new setting.
Do not add a second read semaphore on top of the existing connection limit.

## Completion records

`tables read completed` is emitted once per observed read after its rows,
explicit connection, and operation locks have been released. All eight read
tools are covered, including errors before execution, authorization failures,
iteration errors, and results truncated by a limit.

- `request_id`, `call_id`, `project_id`, `operation`, `query_id`: correlation.
  The versioned fingerprint hashes SQL tokens with comments and literals removed,
  or the typed query argument structure with filter values omitted. It is a
  query-shape identifier, not a correctness key or a full SQL parser. Raw SQL,
  parameter values, row values and raw database error messages are never logged.
- `outcome`, `stage`, `deadline_source`, `error_type`, optional
  `sqlite_error_code`: separate the phase where a failure appeared from the
  deadline that expired. Sources are `read_queue`, `execution`, `metadata`,
  `operation`, `upstream`, and `caller_cancel`. Non-timeout errors have no
  deadline source. A timeout in `select` or `scan` is not a pool-wait failure.
- `prepare_ms`, `schema_queue_ms`, `metadata_ms`, `read_queue_ms`,
  `connection_setup_ms`, `authorization_ms`, `count_ms`, `select_ms`, `scan_ms`,
  `cleanup_ms`, `hydrate_ms`, `total_ms`: wall-clock phase durations.
  `select_ms` covers the initial database call. `scan_ms` includes further SQLite
  execution in `Rows.Next` plus Go scanning/result construction. `sql_ms` is the
  sum of count, select and scan, not SQLite CPU time. Metadata operations use
  the pool directly, so `metadata_ms` includes their implicit connection wait;
  `read_queue_ms` measures the explicitly acquired main query connection only.
- `execution_budget_ms`: remaining deadline when the execution context is
  created; an earlier upstream or operation deadline can shorten it.
  `deadline_overrun_ms` measures completion after the expired deadline. A
  cancellation signal has no reliable timestamp here, so caller cancellation
  does not report an overrun duration.
- `rows_returned`, `rows_materialized`, `truncated`: output size in rows, never
  rows examined by SQLite. Partial materialization is captured for raw queries
  and available typed scan results; errors can discard partial typed results.
  A one-row aggregate can still examine millions of rows.
- `pool_max_open`, `pool_in_use_start`, `pool_in_use_acquired`,
  `pool_in_use_end`, `pool_idle_end`, `connection_acquired`,
  `shared_writer_pool`: effective pool state. These are process-wide snapshots;
  another request may acquire a just-released connection before the end sample.
  `connection_acquired` refers only to explicit query acquisition, not metadata.
- `pool_wait_count_total`, `pool_wait_ms_total`: cumulative database/sql pool
  counters. Differences over a measurement interval describe pool pressure;
  do not attribute them to an individual request.

The mount log also reports the actual pool maximum, whether it shares the writer,
Go's effective `gomaxprocs`, and visible logical CPUs. These do not replace
container CPU quota/throttling metrics.

## Confirmed cancellation limitation

The additional tests exposed a separate issue with iteration cancellation in the
pinned driver. A synthetic query returns its first row quickly, then performs a
large computation for the next row. Cancellation is not promptly interrupting
that later computation. This is reproduced without any Tables code:

```sh
GOWORK=off go run ./scripts/reproduce-read-cancellation.go
```

A local run on 2026-09-08 reported a **20 ms deadline, 573.4 ms elapsed**, one row
read, `context deadline exceeded`, and zero connections still in use after
completion. A subsequent query reused the connection successfully. The script
uses a finite computation in a disposable in-memory DB and exits 1 when return
exceeds 250 ms. Hardware changes the duration; it is a diagnostic, not a benchmark.

In modernc.org/sqlite v1.50.0, the cancellation watcher in `stmt.query` stops
when `QueryContext` returns, while `rows.Next` can perform later SQLite steps.
See the pinned module's `stmt.go` and `rows.go`, and
[the driver's source](https://gitlab.com/cznic/sqlite/-/tree/v1.50.0).

This does **not** prove that the reported Dashboard/Pilotage failures used this
path. It does mean that logging an expired context alone is insufficient proof
that database work stopped at the deadline. The application tests deliberately
use finite work and verify accurate diagnostics and eventual release; they do
not claim prompt interruption during iteration.

A driver-level fix should keep cancellation active through result iteration and
synchronize watcher shutdown before a connection is reused. It needs regression
coverage for both prepared/direct queries, EOF, explicit Close, truncation,
cancellation during a later step, and a follow-up query on the same connection.
This patch does not vendor, fork, or access private driver internals to implement
that fix. Scheduling changes would not repair this cancellation path.

## Validation of this change

Local validation used Go 1.26.6 on darwin/arm64 with `GOWORK=off` to retain the
release's pinned SDK and driver. The declared Go 1.25.12 toolchain was unavailable
in the local cache. The full Tables test suite passed, as did the added read
checks under `-race`. Coverage includes SQL fingerprint redaction, one completion
record per logged request, all typed read tools, queue/schema/metadata/SQL
failure distinctions, upstream and operation deadline sources, truncation,
cancellation of a full read pool, and subsequent connection reuse.

The standalone cancellation reproducer intentionally exits nonzero when it
observes delayed interruption. That known finding remains separate from the
passing application diagnostics tests.
