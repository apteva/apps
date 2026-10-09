# Tables 0.2.12 projection index ordering

Validated locally on 2026-10-09, Darwin arm64 / Apple M1 Pro, Go 1.25.12,
SDK v0.97.0, SQLite v1.50.0, in an isolated checkout and disposable WAL databases.
No production instance or database was accessed.

Release checks passed: `GOWORK=off go test -race ./...` (230 seconds),
focused race regressions, `go vet ./...`, standalone Darwin arm64 and Linux
amd64 builds, 16 Bun UI tests (74 assertions), UI type checking with the shared
workspace ui-kit path, production UI bundle build, and `scripts/smoke.py`
against the compiled sidecar. The smoke test covers public HTTP/MCP index
replacement, restart, inherited replacement builds, activation and index drop.
Go checks used the pinned public SDK with the workspace overlay disabled.

`projection_index_layout_test.go` checks 10,000 source rows, 50 scopes and 11
simultaneously published generations. The default generation-leading indexes
produce `SCAN d`; replacements using `filter_first` produce indexed searches on
`centre_id` plus timestamp bounds, and on `sale_id,call_id`. Results are identical
before/after, including untouched and partially refreshed scopes. Explicit staged
rows and obsolete scoped rows remain invisible. Internal generation searches
continue to support bounded cleanup; source changes invalidate and publish normally.

Coverage also checks actual SQLite index column ordering/directions against API
metadata, legacy blank physical identities, failed swaps after new-index creation
(complete rollback), unique scope/generation constraints, unsupported options,
replacement preserving layout, inheritance before activation, incompatible schema
and unique scope inheritance failures, SQLite file reopen, index drop after
restart, and additive migration from the exact migrations 001–015 without reindexing.
A one-millisecond write-budget test cancels a 10k-row replacement, retains the old
index, and permits the next ordinary source write (14.7 ms observed cancellation
return time locally; context interruption is not a real-time deadline guarantee).

The mixed workload now includes indexed projection API reads alongside source
list reads, background calculation/publication and an event burst. The existing
20k-row staging/cancellation/atomic-publication regression maintains a filter-leading
index, checks that readers see complete generations, and observes a source update
during staging. That source update completed in 6.1 ms in the local race run.

Fixed-size benchmark samples (three trials each):

| Workload | Generation first | Filter first |
|---|---:|---:|
| API centre/date query, 10k rows / 11 published generations | 1.12–1.16 ms | 0.28–0.30 ms |
| API sale/call identifier query, same fixture | 1.20–1.24 ms | 0.24–0.25 ms |
| Transactional index replacement, 10k rows | 14.3–16.0 ms | 9.8–11.5 ms |
| 20k-row full refresh with two result indexes and retired-generation cleanup | 0.87–1.55 s | 0.78–0.85 s |

Reads use 100 iterations per trial; index builds use 10. Publication uses two
iterations per trial, includes calculation, index maintenance, staging, publication
and cleanup, and keeps source/retained storage fixed. With no user result indexes,
publication measured 0.70–1.01 s. Small trial counts and host activity limit inference:
these are local controlled observations, not production latency/capacity guarantees.
SQLite index builds hold the writer lock and use `max_write_ms`; the layout option
does not make index creation online. For large datasets, build inherited indexes
on the empty replacement version before its result is populated when practical.

# Tables 0.2.11 verification cache and watched dependencies

Validated on 2026-10-08 in an isolated checkout using disposable databases,
Darwin arm64 / Apple M1 Pro, Go 1.25.12, SDK v0.97.0, SQLite v1.50.0.

Release checks passed: `GOWORK=off go test -race ./...`, focused race
regressions, `go vet ./...`, standalone Darwin arm64 and Linux amd64 builds,
16 Bun UI tests, UI type checking with the shared workspace ui-kit path,
production UI bundle build, and `scripts/smoke.py` against the compiled sidecar.
All checks used the pinned public SDK with the workspace Go overlay disabled.

New regression coverage in `sql_verification_cache_test.go` and
`projection_watched_test.go` checks ordinary/JSON verification reuse, caller and
projection permission revocation after warming, DDL/index/projection lifecycle
invalidation, bounded caches, failed verification rejection, and database/epoch
changes. Watched dependency coverage includes unrelated metadata/no-op updates,
NULL transitions, same-scope aggregate changes, old/new scopes, inserts/deletes,
remapping, independent building/current watch lists, legacy defaults, in-flight
follow-up work, closing/reopening SQLite, and migration from migrations 001–014
with a pending legacy change record.

The five-source joined test uses 10,000 historical calls, skips metadata updates
across all five sources without changing readiness, coalesces 2,000 relevant sale
updates into one scope, applies prospect/offer dependency changes, and compares
all published aggregates with a forced full rebuild. The existing 100,000-call,
10,000-event regression still checks indexed inner scopes and full correctness.
The real sidecar HTTP/MCP smoke test exercises saved watch configuration,
metadata skipping, actual worker publication and process restart recovery.

`BenchmarkOrdinarySQLVerification`, three one-second samples per mode:

| Verification mode | Time per operation |
|---|---:|
| Cold successful program verification | 58.7–61.4 µs |
| Warm successful verification | 13.2–14.7 µs |

This measures validation only, including current placeholder checks and physical
root lookup. It does not execute the aggregation or measure end-to-end refresh
latency. The fixture is an ordinary grouped SELECT with a bound predicate; these
numbers do not quantify the reported 1.4-second spike on the user's workload.

# Tables 0.2.0 projection validation

Validated on 2026-10-05, Darwin arm64 / Apple M1 Pro, Go 1.25.12,
SDK v0.95.0, SQLite v1.50.0. All execution used isolated checkouts and disposable
databases. No production API, database, deployment or instance was accessed.

## Correctness and lifecycle

`projection_trial_test.go`, `projection_resources_test.go` and the original
projection regression suite cover:

- Five joined sources (calls, sales, prospects, campaigns, offers), materialized
  CTEs, COUNT DISTINCT, conditional SUM, AVG, revenue and discount aggregates.
  Every aggregate is compared against the direct scoped SQL result.
- 100,000 historical calls outside the target prospect and 20 target calls.
  EXPLAIN QUERY PLAN must show indexed SEARCH operations for calls and sales,
  and must not show full scans of their physical source tables.
- 10,000 relevant sale updates coalescing to one dirty scope; campaign/offer
  dependency invalidation, sale deletion and old/new centre invalidation.
- Timezone-derived days, DST bounds, fractional timestamps at the start of the
  local day, canonical numeric/boolean scopes and safe scalar binding.
- Persisted intervals, non-sliding deadlines under continuous changes, clean vs
  dirty scope readiness, unrelated changes, explicit force during a claim,
  consumed vs published watermarks, full/scoped snapshots ahead of log
  consumption without redundant refreshes or lost manual requests, reserved
  revision changes, and reopening the actual SQLite file.
- Initial not-ready state, building versions, activation gates, failed
  replacements, source changes during builds, index retention/version selection,
  safe result writes and coverage retained through failure.
- Single background connection independent from SDK readers, invisible staging,
  readers seeing only complete generations, source updates between bounded
  writer batches, cancellation during staging and successful retry.
- SQL deadlines including expensive later result rows, byte/row caps, retry
  backoff, lease fencing, bounded cleanup and conservative whole-rebuild fallback
  when invalidation lookups fail.
- Raw storage denial, projection index permissions, snapshot status/data batches,
  preventing batch-schema caches from enabling result row writes, and migration
  from the exact released SQL schema with pruned pending watermarks.

The joined/scoped calculation demonstrably uses a prospect index before CTE
materialization. It does not rely on an outer result filter for this acceptance
case. SQL authors must still provide equivalent scoped SQL and suitable indexes
for their own workloads.

## File-backed benchmarks

Benchmarks use WAL databases, a fixed source fixture, complete queue draining,
and reclamation of retired rows. They include index maintenance during refresh.
The prior growing-source benchmark that ran only two worker ticks was removed.

One local run, three iterations per case:

| Workload | Result indexes | Mean duration |
|---|---:|---:|
| 1,000 relevant changes + one joined scope; 100,020 source calls | 0 | 110 ms |
| Same joined scope/event burst | 2 | 37 ms |
| Full 20,000-row generation, including cleanup | 0 | 1.27 s |
| Same full generation | 2 | 1.45 s |

Three iterations provide an illustrative smoke measurement, not a capacity
estimate. Warmup and host activity materially affect these numbers; earlier runs
measured 40–45 ms for the joined burst and 0.27–0.54 s for full publication. The
indexed joined case is not evidence that result indexes accelerate refresh.
The larger full-result case shows why index maintenance must be measured.

In the concurrent file-backed correctness test, an ordinary source update during
indexed 20,000-row staging completed in approximately 0.4–0.8 ms on local runs.
Readers retained the previous result until the atomic switch. This is one local
latency observation, not a production latency guarantee.

## Reproduce

```sh
env GOWORK=off go test ./...
env GOWORK=off go test -race ./...
env GOWORK=off go vet ./...
env GOWORK=off go test -run '^$' -bench '^BenchmarkProjection' -benchtime=3x -count=1 ./...
env GOWORK=off go build -o /tmp/tables-darwin .
env GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/tables-linux .
```

The full race suite passed locally (139 seconds); focused race checks cover the
final timestamp, force, metadata and invalidation-fallback changes. Standalone
Darwin/Linux builds and vet passed. CI runs the full race suite, vet and portable
builds on Tables pull requests and release tags.

SDK v0.95.0 is the latest tag on the remote default branch's commit ancestry
(`origin/main` points at its tagged commit; v0.94.0 is an ancestor). The separately
tagged v0.82.1 branch is not on that ancestry. The SDK pin remains v0.95.0.

## Operational bounds

Default background concurrency/read budget is one, writer batches are 128 rows /
256 KiB, and each publication transaction is capped at 500 ms including queue
wait. The complete refresh defaults to 30 seconds, 1 million result rows / 64 MiB,
with a separate 100,000-row per-scope ceiling. Limits include calculation,
decoding, staging and publication. Large or invalid builds fail safely and keep
the previous complete result. Scope/full switching is atomic, while staging and
cleanup use separate bounded transactions.

Coverage is declared for the SQL's actual fixed window and becomes visible only
with successful publication. Rolling-window inference and arbitrary-SQL query
plan optimization are not automatic. See PROJECTIONS.md for configuration and
restore-required downgrade instructions.
