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
  consumed vs published watermarks and reopening the actual SQLite file.
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
