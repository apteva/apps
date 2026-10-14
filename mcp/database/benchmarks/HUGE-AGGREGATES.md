# Million-row aggregate measurements

Historical baseline before the adapter optimizations. See [current adapter results](ADAPTER-AGGREGATE-OPTIMIZATION.md) for projected decoding, parallel aggregation and configurable memory budgets. The samples below are preserved for comparison.

13 October 2026. Database development branch `fix/database-native-performance`, with AptevaDB pinned to `v0.2.0-alpha.3`. Production engine code is unchanged from the integration benchmarks. Added an opt-in `aggregatebench` test harness.

**One million input records aggregate successfully on all three backends. AptevaDB is fastest for native-compatible numeric metrics and grouping, but its checked integer and nullable-count fallbacks are slower.** High group cardinality remains bounded by the embedded adapters' 16 MiB group-state budget.

## Measured latency

One fresh process and disposable database per backend, sequentially SQLite → AptevaDB → Pebble. Each of the five successful query shapes was executed five times on the same loaded database. Timings include `Manager.Execute` and JSON result encoding, with value checks outside the timer. Values below are **median milliseconds**, with the minimum and maximum of the five executions in parentheses. These are warm-cache repetitions, not independent dataset reloads or cold-storage tests.

| Aggregate over 1,000,000 records | SQLite | Pebble | AptevaDB |
|---|---:|---:|---:|
| Numeric count/sum/avg/min/max, four groups | 717.1 (687.8–877.4) | 706.9 (699.5–1318.6) | 175.8 (168.9–181.0) |
| Same numeric metrics, 1,000 groups | 4184.8 (4038.3–4301.0) | 745.2 (729.3–769.9) | 177.8 (176.7–185.6) |
| Exact integer metrics, four groups | 618.6 (598.2–658.8) | 789.4 (781.2–803.6) | 2221.7 (2189.7–2502.2) |
| Nullable-field counts, four groups | 564.9 (545.7–599.1) | 655.0 (650.2–662.2) | 2033.4 (2009.3–2168.5) |
| Indexed tenant filter: 1,000 input rows | 5.4 (4.7–5.6) | 15.3 (14.1–19.9) | 1.8 (1.6–1.9) |

Dataset: fixed-width text IDs, `n` as a number, `i` as an exact integer wire string, four buckets, 1,000 tenants, a nullable text field (20% null), and 128 bytes of padding plus service metadata. There is one primary index and one non-unique tenant index. No extra index was added on the four-group bucket. The indexed-tenant query filters exactly 1,000 records from the million before aggregating.

All group counts, sums, averages, minima and maxima were checked against arithmetic expected values for every returned group. Integer results must match exact decimal strings; nullable counts exclude every null. All 84 executions behaved as expected, including the explicit memory-limit cases. Successful broad queries completed within the app's default 30-second query deadline. `timeoutMs` may extend it to 60 seconds, subject to an earlier caller deadline.

AptevaDB processes the numeric workloads in roughly 0.18 seconds, approximately 5.6 million input rows per second here. Its streaming native pipeline avoids per-record map decoding. The integer/nullable workloads use the portable checked path, which decodes full native documents. Each such native fallback execution allocated about **2,644 MiB cumulatively**, despite retaining only four groups; this is allocation traffic, not live heap or a 2.6 GiB retained working set. Native checked integer accumulation and conditional non-null field counts would enable these operations to use the fast path.

The 1,000-tenant SQLite timing is specific to this schema and index layout. Its aggregate query plan was not captured in this pass; the cause of its higher cost needs a separate plan check. This is not a universal cost per group.

## A million distinct groups

An additional query groups by the unique primary ID and sorts by aggregate count before returning the top one. It was executed three times per backend, checking the result or required resource error. A result `limit: 1` does not reduce the number of groups accumulated by the embedded hash aggregators.

| Backend | Outcome | Median ms |
|---|---|---:|
| sqlite | success | 360.4 |
| pebble | resource_limit | 25.7 |
| aptevadb | resource_limit | 13.1 |

SQLite's native SQL execution can handle this query without the adapter retaining a million result maps. Its VM/internal temporary state is not governed by the app's embedded group-state accounting limit. AptevaDB and Pebble reject too many retained groups with `resource_limit` and return no partial aggregate. The time for rejection is **not successful aggregate latency**. Larger group cardinalities on those paths need streaming ordered grouping, spill-to-disk aggregation or an explicit configurable budget; raising a result limit alone does not solve it.

## Process and storage footprint

These measurements cover loading plus all reads. Peak RSS is a process maximum, not the retained size of a single query. Live file sizes include recovery logs and the app registry, without forced compaction. Different cache and storage/compression implementations apply.

| Backend | Process peak RSS MiB | Live file bytes MiB | Load seconds |
|---|---:|---:|---:|
| sqlite | 41.9 | 304.7 | 33.8 |
| pebble | 64.5 | 94.1 | 46.1 |
| aptevadb | 491.5 | 455.5 | 12.1 |

AptevaDB's default cache configuration targets speed and uses substantially more resident memory in this run. The query-state budget is not a process RSS cap. A cache-constrained or disk-bound comparison would be a separate experiment.

## Scope and reproduction

Apple M1 Pro, 10 logical CPUs, 16 GiB RAM, macOS arm64, Go 1.25.1, CGO disabled. Background desktop/storage activity was present. Each backend owned its temporary data until it finished, then removed it; no installed app data was opened. The numeric dataset differs from the preceding 100k/three-secondary-index matrix, so those samples are not pooled here. Current native-compatible broad aggregation uses the single-thread `Collection.Run` executor; parallel native aggregation was not measured in this pass.

The harness was compiled and checked with `go vet -tags aggregatebench ./engine`. A separate 100k AptevaDB smoke run checked all metric and resource-limit expectations before the million-row runs. The native integer-overflow regression and fallback correctness are also covered by the earlier full contract/race suite.

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
 DB_HUGE_AGG_ADAPTER=aptevadb DB_HUGE_AGG_ROWS=1000000 \
 DB_HUGE_AGG_OUTPUT=/absolute/path/to/new-result.json \
 go test -tags aggregatebench ./engine -run '^TestHugeAggregateBenchmark$' -v -count=1 -timeout=20m
```

Repeat with `sqlite` and `pebble`, using separate output files. The test refuses existing output files, uses one database per process and stops loading if available disk falls below 256 MiB.

- Summary data (local historical artifact: `database-huge-aggregates-2026-10-13/summary.json`)
- SQLite raw samples (local historical artifact: `database-huge-aggregates-2026-10-13/sqlite-1000000.json`)
- Pebble raw samples (local historical artifact: `database-huge-aggregates-2026-10-13/pebble-1000000.json`)
- AptevaDB raw samples (local historical artifact: `database-huge-aggregates-2026-10-13/aptevadb-1000000.json`)
- [Benchmark source](../engine/huge_aggregate_benchmark_test.go)

## Engine versus adapter follow-up

A direct alpha.3 probe with two int64 maximum values and one null confirms native `$sum` returns `-2` and native `$avg` returns `-1`. The average must avoid overflowing an int64 intermediate accumulator; sum needs an explicit checked/wider accumulation policy, including parallel merges. The adapter currently avoids both native integer paths and remains protected.

Native `$count` counts rows, including null-valued rows, even when supplied a field reference. SQL-like non-null field counting is an additional capability the app needs; row-count behavior should remain available. It can be implemented natively or translated by the adapter into filtered native counts plus merging groups.

The measured fallback overhead is in our adapter: it decodes every document before selecting the metric/group fields. Projected decoding or native checked reducers would remove much of the allocation traffic. The adapter also uses `Run` while the library already provides `RunParallel`, and opens `DurableOptions()` without specifying the available `CacheBytes` setting. Parallel execution and configurable cache/query budgets are adapter improvements to evaluate, rather than missing library APIs.

The 16 MiB query/group budget is set by the adapter. Increasing it is an adapter policy change. Bounded-memory processing of millions of groups requires streaming ordered aggregation or disk spilling in the execution engine.

Direct engine probe output (local historical artifact: `database-huge-aggregates-2026-10-13/aggregation-gaps.json`) · Reproduction source (local historical artifact: `database-huge-aggregates-2026-10-13/aggregation-gaps.go`)
