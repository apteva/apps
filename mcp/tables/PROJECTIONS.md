# SQL projections in Tables 0.2.12

A projection stores a complete published result in Tables. Source writes append
small transactional change records; a worker consumes them and recalculates dirty
scopes. Business rules belong in the project's SQL and dependency mappings.

## Restrict the calculation before aggregation

`sql` calculates the complete projection. `scope_sql` calculates one dirty scope.
All SQL is read-only and can reference only declared `source_tables` using
`{name}` placeholders. Positional `?` values are bound, never inserted into SQL.
`scope_params` supplies scope values in order, followed by fixed `params`.

```json
{
  "name": "prospect_stats", "version": 1,
  "source_tables": ["calls", "sales"],
  "scope_columns": ["prospect_id"],
  "result_columns": [
    {"name": "prospect_id", "type": "number"},
    {"name": "call_count", "type": "number"},
    {"name": "revenue", "type": "number"}
  ],
  "sql": "WITH relevant AS MATERIALIZED (SELECT * FROM {calls}), revenue AS MATERIALIZED (SELECT prospect_id,SUM(amount) AS amount FROM {sales} GROUP BY prospect_id) SELECT c.prospect_id,COUNT(*) AS call_count,COALESCE(MAX(r.amount),0) AS revenue FROM relevant c LEFT JOIN revenue r ON r.prospect_id=c.prospect_id GROUP BY c.prospect_id",
  "scope_sql": "WITH relevant AS MATERIALIZED (SELECT * FROM {calls} WHERE prospect_id=?), revenue AS MATERIALIZED (SELECT prospect_id,SUM(amount) AS amount FROM {sales} WHERE prospect_id=? GROUP BY prospect_id) SELECT c.prospect_id,COUNT(*) AS call_count,COALESCE(MAX(r.amount),0) AS revenue FROM relevant c LEFT JOIN revenue r ON r.prospect_id=c.prospect_id GROUP BY c.prospect_id",
  "scope_params": ["prospect_id", "prospect_id"],
  "min_refresh_interval_seconds": 30
}
```

Both sources have `prospect_id`, so direct invalidation is sufficient here.
Create source indexes on `calls(prospect_id)` and `sales(prospect_id)` using
`indexes_create`. `scope_sql` must return the same declared schema as `sql`, and
every returned row must belong to the requested scope. An empty scoped result
removes previously published rows for that scope.

Without `scope_sql`, Tables retains the existing outer-filter behavior. Complex
CTEs need restrictions inside their saved SQL to avoid calculating history before
filtering. Tables validates access and binding; the SQL author controls the query
plan and equivalence of full/scoped calculations.

## Derived scopes and dependency lookups

Sources without every scope column require explicit `scope_rules`. Each rule maps
every result scope from captured fields, or from a bounded dependency SELECT.
`params` on a rule names fields captured from the changed source. Rules are
applied to both old and new source values for insert/update/delete invalidation.
Multiple rules may target one source. Lookup SQL follows the same declared-source
and authorization rules as projection SQL.

```json
{
  "scope_rules": [
    {
      "source_table": "calls",
      "values": {
        "centre_id": {"column": "centre_id"},
        "prospect_id": {"column": "prospect_id"},
        "day": {"column": "event_at", "bucket": "day", "timezone": "Europe/Madrid"}
      }
    },
    {
      "source_table": "sales",
      "sql": "SELECT DISTINCT centre_id,prospect_id,event_at FROM {calls} WHERE prospect_id=?",
      "params": ["prospect_id"],
      "values": {
        "centre_id": {"column": "centre_id"},
        "prospect_id": {"column": "prospect_id"},
        "day": {"column": "event_at", "bucket": "day", "timezone": "Europe/Madrid"}
      }
    }
  ]
}
```

Declare `day` as a text result/scope column. `day` and `month` buckets require an
explicit IANA timezone. Identical dependency lookups are cached within each
consumed change batch. Return distinct scopes to bound fan-out: each lookup may
return at most 4,096 rows, and each change may invalidate at most 4,096 scopes.
If a lookup fails or exceeds fan-out, Tables records the failure and conservatively
queues a bounded whole rebuild; a batch affecting more than 256 scopes is also
coalesced into one rebuild when that is cheaper. Refresh failures use the same
retry backoff.
Index lookup joins and source filters. A dependency rule can map prospect campaign
changes or campaign/offer changes to dependent calls without app-specific code.

For UTC timestamp filtering by a local day, a scope parameter can bind its bounds:

```json
{
  "scope_params": [
    "prospect_id",
    {"scope_column": "day", "boundary": "start", "timezone": "Europe/Madrid"},
    {"scope_column": "day", "boundary": "end", "timezone": "Europe/Madrid"}
  ]
}
```

Use `event_at >= ? AND event_at < ?` in `scope_sql`. Bounds use normalized UTC timestamps with nine fractional digits, matching
Tables datetime storage. They use the next local calendar day, including
23/25-hour daylight-saving days. The saved full SQL owns
the matching timezone-aware grouping logic; Tables does not rewrite SQL dates.

## Watched source columns

Optionally supply `source_dependencies` when creating an immutable projection
version. Keep `source_tables` as the complete list of sources.

```json
{
  "source_dependencies": [
    {"table": "calls", "watched_columns": ["prospect_id", "centre_id", "started_at", "duration_seconds", "status"]},
    {"table": "sales", "watched_columns": ["prospect_id", "amount"]}
  ]
}
```

An UPDATE invalidates this version only when a watched value actually changes
(NULL transitions count). Unrelated metadata changes and assignments of the same
watched value do not enqueue work or advance `latest_relevant_change`. Changes to
amount/duration still invalidate unchanged scopes. INSERT and DELETE always
apply dependency rules; scope moves invalidate both old and new scopes.
Different versions may watch different columns on the same source.

**The SQL author must include every source input affecting calculations,
filters, joins, and dependency lookup results.** Tables validates column names,
rejects empty/duplicate lists and undeclared sources, and requires direct scope
inputs, derived scope inputs, and mapping parameters in each configured list.
It does not infer arbitrary SQL dependencies. Include `id` for join keys/mapping
parameters where needed, and `_revision`, `created_at` or `updated_at` only if
those fields affect the result. An incomplete SQL watch list can leave results
stale. A source with no watch entry retains existing all-column invalidation.

Describe/list expose the saved configuration. To change it, create and build a
replacement version, then activate it using the normal readiness checks.
The source transaction stores affected version IDs alongside captured old/new
mapping values. Queue coalescing, in-flight follow-up work, intervals and restart
recovery remain durable. Migration 015 only adds a nullable log column; pending
records from previous versions are conservatively relevant, and startup rebuilds
triggers automatically. No source/result row rewrite is required.

## SQL verification reuse

Ordinary SELECTs, projection calculations and dependency queries now reuse
successful SQLite program verification as well as parsing/prepared statements.
Current project/placeholder access and projection permissions are checked on
every request before a verification hit is accepted. Failed verifications are
never saved. The bounded cache is keyed by resolved SQL, database generation and
schema/projection epoch; table/index and projection lifecycle changes invalidate
it. In-flight verification cannot populate a newer epoch after invalidation.

## Scheduling and forced work

`min_refresh_interval_seconds` defaults to **0**, preserving existing scheduling.
Set 30 for statistics when that freshness is sufficient. Source changes are
captured immediately, and the queue coalesces a scope. Its first eligible deadline
stays fixed while more changes arrive; traffic cannot debounce it indefinitely.
The interval runs from the last successful publication that included the scope.
A full build also satisfies captured scope requests covered by its snapshot,
while revision fencing preserves requests that arrive during the build.

Use `projections_update` with `name`, optional `version`, and
`min_refresh_interval_seconds` to persist/reschedule the interval.
`projections_refresh` queues a `scope`, or `rebuild: true` for all scopes.
Administrators with `projections.manage` may pass `force: true` to bypass the
interval. Force preserves a request arriving during an existing claim; it does
not bypass pause, leases, authorization or resource limits. Failed refreshes
retry with exponential 1–60 second backoff. Queue state and deadlines survive
restart; crashed claims become eligible after their fenced lease expires.

## Indexes and protection

Use `indexes_create`, `indexes_list` and `indexes_drop` with the logical projection
`table` name. Add `version` to manage a replacement before activation.

For selective centre/date or relationship filters, nonunique projection indexes
can preserve the requested leading columns and append generation:

```json
{"table":"call_stats","name":"by_centre_date","columns":["centre_id","event_at"],"layout":"filter_first"}
```

This creates `(centre_id, event_at, _projection_generation)`. Column direction
(`{col, order}`) is preserved. Omitting `layout` keeps the legacy
`generation_first` layout, which prefixes `_projection_generation`; existing
indexes are never automatically rebuilt. `indexes_list` and create responses
report both `layout` and `physical_columns` in their actual index order.
Normal table indexes do not accept projection layout/replacement options.

To convert an existing **nonunique** index without first removing it:

```json
{"table":"call_stats","name":"by_centre_date","columns":["centre_id","event_at"],"layout":"filter_first","replace":true}
```

Tables creates a distinct physical index while retaining the previous one, then
atomically records the new definition and removes the old index. A failed or
canceled replacement rolls back completely. `replace` requires an existing index;
omitting `layout` on replacement preserves its selected layout. SQLite index
creation holds the writer lock and may delay source writes. Builds respect the
existing `max_write_ms` operation budget (default 30 seconds), including queue
wait and calculation; it is not an online/batched SQLite index build.

Indexes survive scope/full refreshes and database restarts. Replacement
**projection versions** can use `inherit_indexes: true` on `projections_create`
to copy current index columns, directions, uniqueness and layouts into their empty
result table before building/activation. Incompatible columns or unique scope
constraints reject creation atomically. Inheritance defaults to false, preserving
existing workflows that create indexes explicitly on each version. New physical
identities belong to the new version; the current version remains unchanged.

Unique indexes retain their existing generation prefix and must include every
scope column. `filter_first` and `replace` are rejected for unique indexes.
Internal generation/scope indexes remain available for staging, publication and
bounded cleanup. The visible view still checks both scope and generation against
published heads, excluding staged and obsolete rows for all layouts. Index order
allows filter seeks; it does not itself exclude obsolete rows, and the planner's
choice still depends on SQL predicates and data distribution.

Ordinary row tools, including writes inside batches, cannot modify projections.
The same `projections.read`/`projections.manage` permissions apply to inspection
and modification of projection indexes. Raw storage names remain inaccessible.
Migration 016 only adds defaulted index metadata; existing index storage and rows
are untouched. Automatic backward-compatible upgrades remain supported.

## Freshness, readiness and coverage

`projections_status` accepts a name, optional version, and optional scope.
It exposes:

- `built`, `ready`, `stale`, `is_current` and lifecycle `status`.
- `latest_relevant_change`, `consumed_change_id`, `published_change_id` and `lag`.
- `pending_scopes`, up to 256 `pending_scope_keys`, `refresh_running`, and
  `next_scheduled_refresh` (null while paused).
- `last_successful_publication_at`, `last_failure`, and a requested scope's
  `published_generation` (or `last_full_generation` for overall status).
- Published `coverage_from` and `coverage_to`.
- `phase_timings_ms` for queue wait, calculation, publication and cleanup.

Tables also reports `worker_queue`, `read_queue`, `write_lock`, and `staging`
inside `phase_timings_ms` (with matching `*_ms` fields). This separates time
waiting for an interactive connection or projection worker from calculation,
SQLite writer-lock acquisition, bounded row staging, the short generation
publication switch, and old-generation cleanup. The previous `publication_ms`
value remains available as the end-to-end publication phase for compatibility.

Change IDs are watermarks, not counts; unrelated source tables do not increase a
projection's latest relevant watermark. Consumption alone never declares a
result ready. Scope inspection considers that scope and whole rebuild requests;
unconsumed relevant changes newer than the published snapshot conservatively
make scopes stale until mapped. A full build publishes the watermark of its
calculation snapshot even when log consumption is still catching up; those
already covered events do not enqueue redundant automatic refreshes. Explicit
refresh requests are preserved regardless of source watermark.
Initial projections report not ready until the first complete successful build.
A missing scope in a complete build represents an empty result.

After a successful publication Tables emits `projection.ready` with the
projection version, included scope keys, generation and included source
watermark. A single scope also has the legacy `scope_key` field; coalesced
delivery always includes `scope_keys` and `scope_count`. The event is an
invalidation hint; consumers should use status or their next read for
authoritative data. Publications are persisted in an outbox in the same
transaction as the generation switch, delivered in bounded batches, and retried
with exponential backoff after a gateway failure. Stable event IDs make
ambiguous retries idempotent when the platform event API is available.

Declare both coverage bounds as RFC3339 timestamps when SQL covers a fixed window
`[coverage_from, coverage_to)`. Match the SQL's actual restrictions. Coverage is
published with the generation, including empty results, and survives failures.
Bounds are declared metadata, not inferred from SQL or automatically advanced.
For a different window, build a new definition/version with the matching SQL and
bounds. A query outside that window differs from a covered zero result.

`tables_query` returns a `projections` metadata array for referenced projections,
from the same SQLite snapshot as its rows. A `read_snapshot` batch can also
combine a scoped `projections_status` or `projections_describe` with the data
query. GraphQL can choose a previous complete generation or a fallback query
using readiness and coverage. Tables does not decide that application policy.

## Build and activate a replacement

1. Create a new version with `activate: false` and optional indexes.
2. Wait for its worker build and subsequent relevant changes to publish.
3. Inspect `projections_status` for that version; require `ready: true`.
4. Call `projections_activate` with the name and version.

Replacement definitions always build alongside the current version, including
when `activate` is omitted. Activation validates readiness in the writer
transaction and atomically changes the current version. Failed replacements
leave current readers usable. Pausing retains current readers and change
capture. Retired versions cannot resume capture; create a new version instead.

## Resource limits and upgrades

Interactive operations and projection refreshes have separate capacity
reservations. `max_read_conns` limits interactive read slots,
`max_projection_workers` limits concurrent scope calculations, and
`max_total_concurrency` caps their shared capacity while preserving one slot
for each class (when the total allows it). `max_read_queue_ms`
and `max_projection_queue_ms` bound waits for those slots. Increasing read
connections is optional; choose it based on CPU and SQLite workload.

Refreshes use a dedicated bounded read-only background pool for file-backed
databases; its size follows `max_projection_workers`. Interactive SDK readers
remain available. In-memory test fixtures use their shared database pool. Each refresh deadline
covers calculation, decoding, row/byte validation, staging and publication.
A bounded single JSON result keeps all SQLite calculation under the pinned
driver's cancellation watcher; typed cells preserve floating-point precision.

Defaults are `max_refresh_ms: 30000`, `max_result_rows: 1000000`,
`max_result_bytes: 67108864`, `max_publication_ms: 500`,
`publication_batch_rows: 128`, and `publication_batch_bytes: 262144`.
Limits cannot exceed the app's configured ceilings. Each scope also respects
`max_projection_rows` (default 100000). The byte limit covers the bounded
calculation envelope as well as published result data. A row larger than a
publication batch fails safely; increase the batch-byte limit within its 1 MiB
ceiling when appropriate. Writer transactions include connection wait time in
the publication deadline. A pointer switch exceeding its deadline rolls back.

Rows stage invisibly in bounded transactions. A short transaction switches scope
heads only after staging succeeds; readers retain the previous complete result.
SQL parsing and read-only program validation are cached with bounded,
schema-invalidated entries; authorization is still checked on every request.
Prepared read plans are discarded on table DDL and projection activation.
Cleanup uses generation and scope indexes, bounded deletion transactions and a
limited per-tick budget. Excess retired storage applies refresh backpressure
until cleanup catches up.

Migration 010 converts prior physical result tables into generation storage and
stable read-only views atomically, retaining legacy results while rebuilding.
Migration 011 adds persisted phase timings, durable queue millisecond timestamps
and generation-cleanup indexes. Migration 012 adds the projection-ready delivery
outbox and backfills timestamps for older queue rows. Migration 013 adds
capacity and publication timing fields. The worker drains up to 64 scopes per project while staying
within 900 ms per project and 2 seconds globally, then drains a bounded event
batch. These are scheduling budgets; SQL calculation and publication still
honour each projection's refresh limits.
Back up the database before upgrade. **Downgrade to 0.1.27 requires restoring that
backup**; the old worker cannot publish into the new views. The manifest declares
For migrated projections, the app retains a writable `p_<id>` compatibility
table for the previous worker and serves current readers from an atomic
`pv_<id>` generation view. This makes the schema migration
`database_upgrade: backward_compatible` and allows normal blue-green
activation. Releasing a source version does not install or deploy it to
production.

## Worker scheduling and operational metrics

Refresh detection retains a one-second periodic fallback. Local row mutations and
projection management operations also notify a bounded wakeup channel; bursts
coalesce and a change during a running pass retains one follow-up pass. Atomic
write batches notify after commit. Notifications are hints: durable SQLite changes
and queue entries remain authoritative across process restarts and external writes.
Intervals, retry backoff and paused definitions still govern when jobs may run.

Generation reclamation and consumed-change pruning run separately every 45 seconds.
A maintenance pass has a 150 ms total budget, reserves background capacity, rotates
through projections and deletes at most one configured publication batch per
transaction (up to 32 batches per visited projection). Published generations,
published empty heads, active staging leases and valid retired staging leases stay
protected. An empty candidate lookup performs no writer transaction. Pruning checks
for eligible records before writing, retains changes needed by paused consumers,
and removes up to 32 batches of 512 change records within a separate 100 ms
share of the maintenance budget, or 256 expired retired leases per pass.
For a sustained backlog, cleanup throughput remains bounded; capacity planning must
account for refresh rate and result size. Cleanup does not delay refresh detection
until its next scheduled pass.

Worker setup runs once per database identity/generation. A bounded definition cache
uses a durable metadata epoch, including lifecycle/configuration changes from other
sidecars. Publication watermarks, cursors, built state and queues are never cached.
An epoch check inside the invalidation transaction prevents a configuration change
during mapping from advancing a stale cursor. Public SQL permission checks remain
in place. Retiring a version cancels its work and delivery queue; published
historical data remains readable. Pausing retains work, and resume processes it.
Status includes `paused_scopes` and `runnable_pending_scopes`; retired versions are
not operationally ready and cannot be refreshed or resumed directly.

Use `projections_worker_status` or authenticated `GET /projections/worker-status`
for project worker counters. MCP inspection requires project-wide
`projections.read`. `metrics_since: process_start` distinguishes these in-memory
counters from durable per-projection publication timings. Durations use nanoseconds
so cheap idle work does not round down to zero: idle check, change consumption,
capacity wait, read-connection wait, SQL execution, complete refresh, cleanup and
event delivery. SQL execution measures the prepared aggregate's execution/scan;
refresh includes validation, decoding, publication and waits. Those totals overlap,
so do not sum SQL/wait durations with complete refresh duration. Counters include
idle ticks, definition loads/hits, refresh jobs/failures and successful cleanup
batches and confirmed pruned change records. A deadline may race a committed
SQLite deletion, so the latter counts acknowledged deletions and can conservatively
undercount actual reclamation. No SQL text, parameters or row data appear in worker metrics.
