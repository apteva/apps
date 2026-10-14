# Alpha.4 adapter and direct-driver validation

13 October 2026. AptevaDB public `v0.2.0-alpha.4`, source `bff9d0e956989bb566c48addb34377955a402f50`, Go module checksum `h1:wFsuGsJcBM9JGH4/FVqc+9IaJ5oZvaBHlJ3hVuC2xXU=`. Local branch `fix/database-native-performance`, based on Database 0.4.4; app-sdk remains topology-latest v0.82.0. The adapter and standalone sidecar are built locally and unpublished. The installed running app/data have not been replaced.

The [published release](https://github.com/apteva/db/releases/tag/v0.2.0-alpha.4) and [upstream validation](https://github.com/apteva/db/blob/main/bench/results/alpha4/VALIDATION.md) describe exact signed-128-bit integer accumulation/merges, float64 SUM for totals beyond int64, and `$countNonNull`. Our downloaded module resolves to the exact release commit.

## Adapter integration and contract

Native AVG and field counts now use alpha.4's reducers, including `$countNonNull`. Ordinary integer SUM also uses native parallel reduction. The app preserves exact decimal-string int64 sums: a native float64 SUM result fails with `resource_limit` instead of losing precision or panicking.

The app's existing checked accumulator rejects intermediate int64 overflow, even when later inputs would cancel it. Native alpha.4 instead allows that cancellation. To preserve the app behavior, per-group count/min/max bounds certify safe sums for any input order. Existing user metrics are reused when available. Uncertain cases rescan the same snapshot through the checked reducer; pending write overlays keep that reducer. Positive and negative bounds use unsigned division to avoid overflow in the proof itself. Cancellation-heavy values, final overflow, exact >2^53 values, and both metric orders are covered by regressions.

The projected reader was re-audited against the release: `documents.go` is unchanged from alpha.3. Its version guard now recognizes alpha.4 / format 9 and falls back through the public decoder for other versions/layouts. Defaults remain uncapped cache, up to four aggregate workers, and a 16 MiB retained-query budget. Full native durability remains enabled.

## Paired million-row aggregates

Each cell is **adapter / direct**, median milliseconds. Two fresh processes and disposable fixtures per backend, five executions of each successful shape per mode per fixture (10 samples per cell). Both modes share each fixture and alternate execution order. Separate process results and every sample/range are retained. No explicit aggregate warmup/cache flush is used. Timing includes JSON response encoding; result checks are outside the timer.

| Workload over 1,000,000 records | AptevaDB | SQLite | Pebble |
|---|---:|---:|---:|
| Numeric metrics, 4 groups | 45.0 / 45.1 | 694.9 / 701.4 | 693.9 / 685.8 |
| Numeric metrics, 1,000 groups | 50.2 / 55.0 | 4101.0 / 4082.5 | 729.7 / 723.3 |
| Exact integer metrics, 4 groups | 48.1 / 53.7 | 610.3 / 615.1 | 774.6 / 775.4 |
| Nullable-field counts, 4 groups | 51.2 / 50.6 | 560.4 / 562.4 | 644.7 / 643.6 |
| Indexed tenant, 1,000 matches | 1.6 / 1.4 | 4.4 / 4.6 | 14.8 / 11.6 |

All 336 aggregate executions passed their expected result/error checks; an earlier 100k paired smoke adds 168 checks. Expected resource-limit rejections are separate from successful latencies. The dataset matches the previous million-row comparison: typed numeric/integer fields, four buckets, 1,000 tenants, 20% nullable text, 128-byte padding and metadata; primary plus tenant index. Every result's counts, sums, averages and extrema are checked against arithmetic expected values.

Direct SQLite means prepared equivalent SQL through the same `modernc.org/sqlite` Go driver, rather than a separate C SQLite build. Direct AptevaDB means public snapshot collection calls and precompiled pipelines, with the same worker count and memory limit. Output normalization/sorting is still included.

**Pebble has no native aggregation engine.** Its direct column means raw Pebble snapshot/index iterators plus the same projected JSON codec and checked Go reducer. It bypasses manager routing/validation/adapter scan planning while keeping result semantics. It measures a direct-driver baseline, not a built-in Pebble SQL/group operator. Indexed tenant references use each backend's real tenant index. Both modes apply the same deadlines and result/state budgets where supported.

A million unique-ID groups ordered by aggregate count, LIMIT 1, still succeeds in SQLite and fails with `resource_limit` for both adapter/direct AptevaDB and Pebble paths. SQLite's VM/temporary state is outside the 16 MiB accounting policy. Output LIMIT does not limit input groups. No engine spilling or ordered-streaming grouping was added.

The former adapter fallback costs now reduce to native-query scale:

| AptevaDB workload | Alpha.3 optimized adapter medians, ms | Alpha.4 adapter median, ms |
|---|---:|---:|
| Integer metrics, 4 groups | 538.4–580.1 | 48.1 |
| Nullable counts, 4 groups | 407.9–447.4 | 51.2 |

These historical before/after samples use the same schema and machine, but separate run sessions. They are not pooled with current samples. Allocation traffic and whole-process RSS are separately recorded in [summary data](results/database-alpha4-2026-10-13/summary.json); the cache/query budgets are not RSS limits. Similar app/direct medians, including occasional app medians below direct medians, reflect local timing variation and normalization differences rather than a claim that an adapter is intrinsically faster than its storage engine.

## Direct driver matrix at 100,000 records

Each cell is **adapter / direct**; units appear in the workload. Three fresh database fixtures per mode/backend in one benchmark process, with mode/adapter order rotated. Same schema, physical indexes, codec/metadata shape, batch boundaries and durability settings. Native paths assume known schema and normalized inputs; the app includes routing, validation and coordination. Reads are warmed. Input construction and response JSON encoding follow the same timed harness. Point/batch result checks validate retrieved IDs/values, sampled batch endpoints and the full post-write record count. The full API contract tests separately cover affected counts and returned keys.

| Operation | AptevaDB | SQLite | Pebble |
|---|---:|---:|---:|
| Point read, µs | 5.25 / 4.12 | 11.54 / 9.75 | 14.58 / 13.92 |
| Row count, ms | 3.66 / 3.22 | 8.83 / 7.00 | 20.88 / 19.80 |
| Numeric SUM, ms | 3.35 / 3.56 | 12.80 / 13.11 | 41.76 / 46.19 |
| Durable single insert, ms | 4.01 / 4.01 | 0.08 / 0.09 | 4.00 / 4.01 |
| Durable 1,000-row batch, ms | 8.76 / 7.94 | 4.82 / 4.08 | 28.07 / 26.58 |

Within-backend durable settings match. On macOS AptevaDB and Pebble use full flushes; shipped SQLite WAL/FULL has fullfsync OFF, so cross-backend write rankings do not establish equal power-loss guarantees. Numeric SUM now uses the same parallel worker count for both AptevaDB modes; the app's null/overflow contract adds work to its pipeline. This smaller driver fixture differs from the million-row aggregate schema and has no secondary tenant index.

## Compatibility and checks

Full app/backend tests and full race tests passed. Decoder differential fuzzing passed 147510 executions; vet with the aggregate harness passed. Direct serial/parallel probes verify two MaxInt64 values produce a positive widened SUM and an overflow-safe AVG; missing/null fields are excluded from non-null counts while false counts once. App regressions continue to reject out-of-int64 sums and unsafe intermediate cancellation.

A separate genuine alpha.3 fixture was created and copied before opening it with alpha.4. The upgrade preserves documents and supports SUM/AVG/non-null counters at format 9. The alpha.3 binary then refuses it, and all upgraded data/WAL file hashes remain unchanged by that refusal. See [upgrade output](results/database-alpha4-2026-10-13/upgrade-alpha4.json) and [old-binary refusal/hash checks](results/database-alpha4-2026-10-13/upgrade-old-refusal.json). Format 9 permanently upgrades earlier directories: back up with the original binary before deploying an upgrade. All tests/benchmarks here used disposable or backed-up probe data; installed app data was not opened.

Environment: Apple M1 Pro, 10 logical CPUs, 16 GiB RAM, macOS arm64, Go 1.25.1, CGO disabled. Compile/test/fuzz processes finished before timing. Background desktop activity was present. These are local loaded-fixture measurements, not cold-storage, capacity or tail-latency guarantees.

## Reproduction

From the app directory:

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
 DB_HUGE_AGG_ADAPTER=aptevadb DB_HUGE_AGG_ROWS=1000000 DB_HUGE_AGG_NATIVE=1 \
 DB_HUGE_AGG_OUTPUT=/absolute/path/to/new-result.json \
 go test -tags aggregatebench ./engine -run '^TestHugeAggregateBenchmark$' -v -count=1 -timeout=20m
```

Repeat with sqlite/pebble and fresh output files. The [saved runner](results/database-alpha4-2026-10-13/run-aggregates.py) records fixture order and runs precompiled binaries sequentially.

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
 NATIVE_COMPARISON_ROWS=100000 NATIVE_COMPARISON_OUTPUT=/absolute/path/to/new-driver-results.json \
 go test ./engine -run '^TestNativeDriverComparison$' -v -count=1 -timeout=20m
```

Raw data, logs, checksums, compiled binaries and upgrade probes are in [the result directory](results/database-alpha4-2026-10-13). The exact measured executable is saved as `aggregate-bench-measured.test`; the current `aggregate-bench.test` was rebuilt after removal of two unused local-variable lines in the reference harness. Its measured source file is also preserved. The [direct-driver samples](results/database-alpha4-2026-10-13/direct-drivers-100000.json) preserve every trial.

## Million-row sample ranges

Minimum–maximum milliseconds, retaining all executions:

| Backend / mode / workload | Range, ms |
|---|---:|
| aptevadb / app_engine / numeric_four_groups | 43.5–53.6 |
| aptevadb / native_driver / numeric_four_groups | 43.7–51.4 |
| aptevadb / app_engine / numeric_1000_groups | 47.9–56.8 |
| aptevadb / native_driver / numeric_1000_groups | 46.9–61.0 |
| aptevadb / app_engine / integer_four_groups | 46.8–49.2 |
| aptevadb / native_driver / integer_four_groups | 47.1–58.5 |
| aptevadb / app_engine / nullable_count_four_groups | 47.5–59.6 |
| aptevadb / native_driver / nullable_count_four_groups | 47.0–54.5 |
| aptevadb / app_engine / indexed_tenant | 0.9–2.3 |
| aptevadb / native_driver / indexed_tenant | 0.9–1.7 |
| sqlite / app_engine / numeric_four_groups | 679.8–744.8 |
| sqlite / native_driver / numeric_four_groups | 675.0–767.8 |
| sqlite / app_engine / numeric_1000_groups | 3975.8–4256.1 |
| sqlite / native_driver / numeric_1000_groups | 3980.8–4230.7 |
| sqlite / app_engine / integer_four_groups | 597.4–646.9 |
| sqlite / native_driver / integer_four_groups | 600.4–635.1 |
| sqlite / app_engine / nullable_count_four_groups | 541.3–604.6 |
| sqlite / native_driver / nullable_count_four_groups | 538.5–594.1 |
| sqlite / app_engine / indexed_tenant | 4.0–5.3 |
| sqlite / native_driver / indexed_tenant | 3.6–5.1 |
| pebble / app_engine / numeric_four_groups | 685.0–877.2 |
| pebble / native_driver / numeric_four_groups | 675.6–714.1 |
| pebble / app_engine / numeric_1000_groups | 723.4–775.9 |
| pebble / native_driver / numeric_1000_groups | 717.0–818.3 |
| pebble / app_engine / integer_four_groups | 761.2–834.4 |
| pebble / native_driver / integer_four_groups | 761.9–912.4 |
| pebble / app_engine / nullable_count_four_groups | 633.9–655.9 |
| pebble / native_driver / nullable_count_four_groups | 635.9–676.5 |
| pebble / app_engine / indexed_tenant | 8.5–16.4 |
| pebble / native_driver / indexed_tenant | 11.2–13.1 |
