# Local performance benchmark

Run from the Database app directory:

```sh
GOTOOLCHAIN=local python3 benchmarks/run.py
python3 benchmarks/summarize.py benchmarks/results/YYYY-MM-DD
```

The runner builds `cmd/bench` with `CGO_ENABLED=0` and `GOWORK=off`, then launches each trial in a new process. The default matrix is SQLite, Pebble and AptevaDB × 10k, 100k, 1m records × three repetitions. Trials run sequentially; adapter order rotates between repetitions. Each trial creates its own temporary data directory and removes it on normal completion. No existing app data is opened. `--output` selects the result directory; `--rows`, `--repeats`, and sample flags allow smaller follow-up runs. Use a fresh result directory for a different experiment. The runner continues after a failed trial, preserves its log, and exits nonzero after the matrix if any trial failed. `--resume` continues unattempted trials with the same source and binary; completed and failed attempts are both preserved. Trials that fail during loading have a log but no completed result JSON; check `failures.json` alongside the summaries. After a machine restart, use a new output directory and `--trials sqlite-1000000-run2 ...` to select remaining trials; this retains the session boundary instead of pooling changed conditions silently.

## Workload

- Deterministic records: fixed-width IDs, unique email, tenant, status, timestamp, amount, exact integer units, and a 122-byte note.
- 1,000 tenants and four statuses; declared field values are deterministic and contain no user data.
- One automatic primary index and three secondary indexes: unique email, `(tenant ASC, created_at DESC)`, and `(status ASC, created_at DESC)`.
- Bulk load uses atomic 1,000-record batches, with indexes already present.
- 200 timed requests per inexpensive operation, seven per scan/aggregate, and three post-load index-build attempts per trial.
- Indexed point lookup, unique search, filtered/sorted 50-record pages, second-page cursors, filtered and full aggregates, counts, an unindexed last-record search, eight concurrent read clients, single writes, and post-load index creation.
- Filtered aggregates select one tenant: 10/100/1,000 input records at the three dataset sizes. Full aggregates select the whole dataset.
- Updates modify the user timestamp field covered by two secondary indexes. Later single inserts are deleted before index builds, restoring the original cardinality.

Every call goes through `engine.Manager.Execute`. This original matrix harness explicitly imposes a two-second caller deadline, preserving its historical stress profile; the application now defaults broad read queries to 30 seconds. Use the [large-scan verification](LARGE-SCANS.md) to test current read defaults. The measured duration includes request-struct construction, routing, validation, transaction execution, and JSON result encoding. It excludes input JSON decoding, authentication, HTTP, and rendering. These are application-engine measurements, not raw backend microbenchmarks or end-to-end network latency.

Warmups apply to inexpensive and filtered queries. Measurements follow loading and caches are not explicitly flushed; no cold-cache or restart-recovery claim is made. Durable writes retain the app's as-shipped SQLite WAL/FULL and Pebble synchronous-batch settings. No cache, compaction, filesystem, or engine tuning is applied.

## Interpretation

Success latency and failure latency are separate. A fast rejection is never reported as a successful query or index build. Historical results used a 10k-candidate Pebble scan limit; that limit has since been removed. The 10k-existing-record synchronous index-build limit remains on all three adapters. Workloads above that limit still run and record their actual errors.

Returned IDs, counts, group sums/averages, ordering, pagination boundaries, and affected-record counts are checked. Correctness validation runs outside individual request timers. Aggregate expected values are precomputed outside measurements. Per-phase throughput includes loop/check overhead; bulk-load throughput additionally includes input generation. Per-request latency is the cleaner comparison for read operations.

The report records per-request samples, p50/p95, throughput, total allocation deltas, Go heap allocation after each phase, process peak RSS, live file sizes, and file sizes after clean close. Heap-after-phase is not retained heap; peak RSS covers the whole isolated trial, including loading. Disk sizes are logical file bytes, without forced compaction, and include the manager registry. SQLite clean close can checkpoint/remove WAL files. Pebble may retain recovery WALs after close.

`summary.json` and `operations.csv` pool successful latency samples across repetitions, keep errors separate, and take the median of per-trial throughput. Treat the seven-sample scan percentiles cautiously; this is a local smoke benchmark, not a long-duration capacity test. Hardware, source/binary hashes, run order, and flags are saved in `machine.json`.

## macOS single-insert diagnosis

The original settings have different macOS flush strengths. See the [controlled durability comparison](INSERT-DURABILITY.md) before comparing write latency. An opt-in diagnostic measures Pebble preparation/commit time and SQLite with fullfsync off/on.

## Concurrent durable write benchmark

See [Pebble write batching results and reproduction](GROUPED-WRITES.md). This separately compares the original serial write path and the grouped path with the same durability settings.

## Broad-query optimization

See [optimized scan measurements](OPTIMIZED-SCANS.md) for the follow-up to the million-record checks. The `largescan` harness accepts `DB_LARGE_SCAN_REPEATS=3` to repeat each read workload on the same loaded dataset. `DB_LARGE_SCAN_PROFILE_DIR` optionally writes a separate CPU profile for each measured query; leave it unset for ordinary timing runs. Profiling startup/stop is outside the timer, but sampling can affect timing, so profiled measurements are kept separate.

## Direct native-driver comparison

Run the isolated SQLite/Pebble/AptevaDB comparison against direct driver calls:

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
  NATIVE_COMPARISON_OUTPUT=/absolute/path/to/new-results.json \
  go test ./engine -run '^TestNativeDriverComparison$' -v -count=1 -timeout=15m
```

This runs 10k/100k records and three sequential repetitions, alternating app/native order and rotating adapter order. Every fixture owns a disposable temporary directory. Both paths use the same schema, primary index, record metadata, serialized write requests, and backend durability settings. SQLite uses prepared single-row statements inside one transaction per batch. Native Pebble maintains the same primary and unique-owner keys, checks duplicate keys, and commits indexed batches synchronously. The native write response includes the same affected count and returned primary keys as the app response.

The comparison times warm point reads, counts, sums, single inserts, and atomic 1,000-record inserts, including JSON result encoding. Input generation and correctness checks are outside timers. Counts and sums use normalized scalar outputs. Native paths assume normalized records and a known schema; app paths retain validation, scoped routing, and coordination. HTTP, authentication, MCP parsing, secondary indexes, and concurrent clients are excluded. Durability settings match within each adapter; macOS backend flush semantics still differ between SQLite and Pebble.

For a repeatable point-read allocation/CPU profile, use `go test ./engine -run '^$' -bench BenchmarkPointRead -benchmem -benchtime=2s` (with `GOWORK=off`). The matrix is a smoke measurement on an active local machine; inspect per-trial samples and tail latency before treating a small difference as a speedup.

AptevaDB direct writes use the same typed document codec, physical primary keys, native primary unique index and atomic batches as the adapter; direct reads use collection handles. It uses `DurableOptions()` (`SyncFull`). On macOS AptevaDB and Pebble perform full flushes, while the default SQLite WAL/FULL profile has `fullfsync=OFF`. Cross-backend write rankings therefore compare the shipped settings, not identical power-loss guarantees. Read timings and within-backend app/direct comparisons remain separate.

For a separate macOS full-flush SQLite diagnostic, set `NATIVE_COMPARISON_SQLITE_FULLFSYNC=1` and use a separate output file. Both reserved SQLite writer connections are checked for `PRAGMA fullfsync=ON`; the app default is unchanged. Direct AptevaDB sums use a compiled streaming native pipeline with the same 16 MiB budget, and the app uses its native aggregate path for compatible metrics. Alpha.4 enables native averages and non-null field counts; integer sums use conservative range checks and a checked fallback for uncertain cases. Both app and direct AptevaDB sum measurements use the configured parallel worker count.

See [implemented AptevaDB backend and final three-backend results](APTEVADB-INTEGRATION.md). For paired point reads on the same warmed fixture, set `PAIRED_POINT_COMPARISON_OUTPUT` and run `TestPairedPointDriverComparison`; execution order alternates and serialized app/direct responses must match.

See [million-row aggregate measurements and memory-limit checks](HUGE-AGGREGATES.md) for native-compatible, exact integer, nullable-count and high-cardinality workloads across all three adapters.

See [current adapter aggregate improvements](ADAPTER-AGGREGATE-OPTIMIZATION.md) for parallel native scans, projected checked reducers, tuning options and a fresh million-row comparison. The preceding huge-aggregate report is the historical baseline.

See [alpha.4 paired adapter/direct-driver measurements](APTEVADB-ALPHA4.md) for the current million-row comparison. `DB_HUGE_AGG_NATIVE=1` enables paired direct calls on the same loaded fixture; order alternates between samples. `NATIVE_COMPARISON_ROWS=100000` selects the 100k dataset for the point/count/sum/write matrix, retaining its three process repetitions.

See [matched flush-profile comparison](FLUSH-PROFILES.md). `NATIVE_COMPARISON_APTEVADB_SYNC_MODE=full|barrier|os` selects the native primitive for both app/direct modes, and `NATIVE_COMPARISON_SQLITE_FULLFSYNC=1` enables/verifies full flushes on both SQLite writer connections. Both profiles verify SQLite `synchronous=FULL`. `NATIVE_COMPARISON_ADAPTERS=sqlite,aptevadb` selects only those engines for an ordinary-fsync comparison; Pebble's shipped sync still requests a full macOS flush.

## Realtime watches

See [native capture and watcher performance](REALTIME.md) for 100,000-row capture-off/on comparisons with 0/1/10/32 subscribers, matched full/ordinary flush profiles against direct drivers, complete HTTP SSE delivery latency and million-row aggregate checks. Realtime capture currently uses AptevaDB alpha.5; SQLite/Pebble results are query/write reference comparisons, not claims of implemented feeds.
