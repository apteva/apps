# Adapter aggregate improvements

13 October 2026. Local development branch `fix/database-native-performance`, based on Database 0.4.4. AptevaDB remains pinned to `v0.2.0-alpha.3`; its engine source and format are unchanged. The adapter binary is built locally, unpublished, and has not replaced the running installed app.

The adapter now projects fields before materializing native scan records, reuses query-local storage, and uses the existing parallel native executor for compatible broad aggregates. Checked integer sums/averages and nullable field counts preserve the app contract. Indexed filtered aggregates retain their streaming index path. Durable full flushes remain enabled.

The projected reader validates the flat scalar representation emitted by our document codec. It is guarded by dependency version and format 8, and falls back to the public decoder for alternate field orders/layouts, historical wrapped-offset documents or future library versions. Returned strings and JSON own their storage; retained records are copied from the scratch map.

## One million input records

Median milliseconds, with minimum–maximum in parentheses. Each backend uses one fresh process and disposable database, with five executions per successful workload on the same loaded fixture. There is no separate aggregate warmup or cache flush. Native before/after samples are separate dataset loads on the same machine. SQLite and Pebble were rerun with the final source. Timings measure `Manager.Execute` plus JSON result encoding, excluding HTTP/authentication and request parsing.

| Aggregate | AptevaDB before | AptevaDB after | SQLite, fresh | Pebble, fresh |
|---|---:|---:|---:|---:|
| Numeric metrics, 4 groups | 175.8 (168.9–181.0) | 54.5 (52.4–599.9) | 729.5 (653.6–1335.3) | 688.6 (679.0–868.0) |
| Numeric metrics, 1,000 groups | 177.8 (176.7–185.6) | 67.6 (56.2–85.3) | 3943.7 (3887.0–4414.1) | 773.4 (717.6–810.8) |
| Checked integer metrics, 4 groups | 2221.7 (2189.7–2502.2) | 580.1 (545.8–714.6) | 604.4 (595.7–708.5) | 789.1 (774.7–840.4) |
| Nullable field counts, 4 groups | 2033.4 (2009.3–2168.5) | 447.4 (421.1–520.0) | 550.4 (542.6–628.0) | 648.5 (637.8–660.5) |
| Indexed tenant, 1,000 matching rows | 1.8 (1.6–1.9) | 2.0 (1.7–2.1) | 4.6 (4.5–4.8) | 15.3 (14.3–16.6) |

The first numeric four-group execution in the final native profile took 599.9 ms; the remaining four took 52.4–61.7 ms. The table preserves that outlier. The 54.5 ms median describes steady repetitions, and does not promise that latency for the first execution.

The dataset matches the [historical baseline](HUGE-AGGREGATES.md): four buckets, 1,000 tenants, numeric and exact integer values, 20% nullable text, 128-byte padding, primary and tenant indexes. Every returned group's counts, sums, averages, minima and maxima were checked. All 84 final-profile executions passed their result/error expectations; the separate cache profile adds 28 checked executions. These are local warm measurements on an active Apple M1 Pro, macOS arm64, Go 1.25.1, CGO disabled, rather than cold-storage or capacity guarantees.

A second fresh-process native run using the final defaults also passed all 28 result/error checks. It is preserved separately from the main table rather than pooled: peak RSS 498.8 MiB, loading 11.4s. The first numeric four-group execution was 52.9 ms, so the first-run outlier was not reproduced. These two fixtures still do not establish a cold-read guarantee.

| Second native fixture | Median ms (min–max) |
|---|---:|
| Numeric, 4 groups | 50.4 (42.5–52.9) |
| Numeric, 1,000 groups | 52.5 (47.8–54.9) |
| Checked integer, 4 groups | 538.4 (535.5–540.6) |
| Nullable count, 4 groups | 407.9 (407.8–418.8) |
| Indexed tenant | 1.7 (1.6–2.0) |

Second native raw samples (local historical artifact: `database-adapter-optimized-2026-10-13/aptevadb-1000000-cache0-repeat2.json`).

The checked reducers' cumulative allocation traffic dropped about 93%:

| AptevaDB query | Before, MiB allocated | After, MiB allocated |
|---|---:|---:|
| Checked integer metrics, 4 groups | 2644.4 | 190.5 |
| Nullable field counts, 4 groups | 2644.4 | 186.2 |

Allocation traffic is cumulative per request, not retained heap or peak RSS. The numeric four-group query allocates approximately 0.06 MiB after the change.

## Memory policy and cache experiment

App configuration/environment now exposes cache target, aggregate workers and retained query budget. Defaults are cache 0 (uncapped), min(4, GOMAXPROCS) workers (panel default 4), and 16 MiB per-query retained state. Configuration overrides environment. See the [README](../README.md) for names/ranges; `explain` reports effective settings. SQLite and Pebble policies are unchanged.

A positive cache target changes the library's WAL and memtable path as well as its logical page cache. The 64 MiB profile peaked at **862.1 MiB RSS**, versus **490.7 MiB** with cache 0, despite similar aggregate speed. This experiment does not justify enabling the cap by default. The final profile keeps peak memory close to the prior 491.5 MiB baseline. Neither cache targets nor group-state accounting bound RSS.

| Final profile | Process peak RSS, MiB | Live files, MiB | Load seconds |
|---|---:|---:|---:|
| sqlite | 42.8 | 304.7 | 30.2 |
| pebble | 64.7 | 91.3 | 51.6 |
| aptevadb | 490.7 | 455.5 | 14.3 |

RSS includes loading and queries; live files include recovery logs without forced compaction. Native loading took 14.3s versus 12.1s in the historical baseline. The aggregate gains do not establish a write-throughput improvement.

A million distinct primary-key groups with top-1 ordered by aggregate count still exceeds the 16 MiB group-state budget: AptevaDB and Pebble correctly reject with `resource_limit`, returning no partial result. SQLite succeeds through its SQL executor, whose temporary VM state is outside this accounting policy. Raising the retained budget is configurable; result `limit: 1` does not remove the cost of accumulating a million groups. Rejection latency is kept separate in the raw data and summary.

## Validation and reproduction

Full `go test ./...` passed, including all three backend contracts and compiled sidecar HTTP/MCP authorization/scope tests. Full `go test -race ./...` passed again with the final cache default; `go vet -tags aggregatebench ./...` passed. Differential decoder fuzzing passed 138,580 executions over 15 seconds with two workers.

Regression coverage includes projected-value ownership, wide documents, wrapped offsets, alternate encoders, malformed data, deeply nested exact JSON numbers, serial/parallel parity, integer-average overflow protection, cancellation, configurable group budgets, and reopening after cache/worker changes. Fresh standalone sidecar and aggregate benchmark binaries were built.

From the app directory, use a fresh output path:

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
 DB_HUGE_AGG_ADAPTER=aptevadb DB_HUGE_AGG_ROWS=1000000 \
 DB_HUGE_AGG_CACHE_MIB=0 DB_HUGE_AGG_WORKERS=4 \
 DB_HUGE_AGG_OUTPUT=/absolute/path/to/new-result.json \
 go test -tags aggregatebench ./engine -run '^TestHugeAggregateBenchmark$' -v -count=1 -timeout=20m
```

Repeat with `sqlite` and `pebble`; those backends ignore native tuning. `DB_HUGE_AGG_MEMORY_MIB` optionally changes the native query budget. Each run removes its disposable fixture; installed app data is not opened.

Raw results, logs, binaries, source hashes and summary data (local historical artifact: `database-adapter-optimized-2026-10-13/summary.json`) are saved in the result directory (local historical artifact: `database-adapter-optimized-2026-10-13`). The final native profile is aptevadb-1000000-cache0.json (local historical artifact: `database-adapter-optimized-2026-10-13/aptevadb-1000000-cache0.json`); the separate 64 MiB profile remains aptevadb-1000000.json (local historical artifact: `database-adapter-optimized-2026-10-13/aptevadb-1000000.json`).

## Separate AptevaDB work

The remaining engine issues from the direct alpha.3 probe still apply: checked/wider integer sums, overflow-safe averages including parallel merges, and native conditional non-null field counts. Our safe fallback protects those operations. An engine projection API could eventually replace the version-guarded reader. Streaming ordered grouping or disk spilling would support much larger group counts with bounded memory. Further investigation of cache/WAL/memtable RSS belongs to that engine work. Direct probe evidence (local historical artifact: `database-huge-aggregates-2026-10-13/aggregation-gaps.json`).
