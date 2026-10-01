# Database local performance results — 7 September 2026

Benchmarked the actual Database app contract over SQLite and Pebble. SQLite remains a useful default for the tested queries and aggregation. The write comparison needs the durability correction below. Pebble had higher concurrent point-read throughput at 10k/100k and a smaller on-disk footprint for this repetitive synthetic dataset. These observations describe this app implementation and workload, not a universal ranking of the underlying engines.

## Subsequent improvement

Pebble now automatically groups concurrent record writes while preserving synchronous durability. The [controlled follow-up benchmark](GROUPED-WRITES.md) measured 4× throughput at eight writers and 12.4× at 32 writers. The historical tables below predate that implementation change.

The fixed 10k-candidate scan limit has also been removed, and broad queries now default to 30 seconds. The [million-record verification](LARGE-SCANS.md) passed find, count, sorting, and grouped aggregation on both adapters. These historical tables retain the original timings, two-second deadlines, and scan-limit failures.

## Correction: macOS write durability

A follow-up diagnostic found that the original write settings use different macOS disk-flush strengths. With SQLite `fullfsync=ON`, its single-insert median was 4.47 ms versus Pebble’s 4.06 ms; synchronous commit accounted for 94.7% of Pebble insert time. The original timings describe the app as shipped, **not an equivalent-durability comparison**. See the [controlled diagnosis and raw measurements](INSERT-DURABILITY.md).

## Completion and failures

Of 18 planned trial slots, 16 completed and two failed during loading. One additional attempt was interrupted by the computer restart and rerun. The completed trials contain 49,956 timed requests and 377,560 successful correctness assertions. A completed report can still contain explicit operation rejections; those are excluded from successful latency statistics.

- All six 10k and six 100k trials completed before the restart.
- The first SQLite million-record attempt failed during insert batch 658 (zero-based 657), after 657,000 records, with `context deadline exceeded`.
- The second Pebble million-record attempt failed during insert batch 24 (zero-based 23), after 23,000 records, with `context deadline exceeded`.
- The user reported a computer crash. The second SQLite million-record attempt was interrupted during loading, with no complete result. It was restarted in a separate session; its original log is retained.
- Crash causation was not investigated. The timeouts near the crash may be affected by host conditions and are not sufficient to attribute a database defect or the computer crash to an adapter.
- Source and binary SHA-256 hashes match across the restart. Post-restart results are shown individually rather than silently pooled with the earlier session.

## Repeated 10k and 100k measurements

Each cell is **p50 / p95 milliseconds** over successful requests pooled across three independent trials. Each ordinary operation has 600 measured requests; scans/aggregates have 21, concurrent reads 4,800, and index creation nine. Scan percentiles have few samples.

### 10,000 records

| Operation | SQLite p50 / p95 ms | Pebble p50 / p95 ms |
|---|---:|---:|
| Primary-key get | 0.059 / 0.131 | 0.055 / 0.100 |
| Unique email find | 0.075 / 0.189 | 0.087 / 0.220 |
| Compound-index page (50) | 0.530 / 1.200 | 1.397 / 2.499 |
| Cursor page 2 (50) | 0.555 / 1.049 | 1.435 / 3.695 |
| Aggregate one tenant | 0.098 / 0.157 | 0.298 / 0.444 |
| Aggregate whole collection | 7.159 / 11.306 | 230.501 / 356.919 |
| Count whole collection | 0.225 / 0.504 | 225.500 / 235.895 |
| Unindexed find | 3.433 / 12.540 | 227.517 / 273.445 |
| Update indexed timestamp | 0.274 / 0.762 | 6.889 / 16.963 |
| Insert one record | 0.300 / 1.921 | 5.137 / 8.800 |
| Get, eight clients | 0.389 / 1.573 | 0.214 / 0.737 |
| Create index after loading | 70.342 / 358.076 | 86.816 / 117.783 |

### 100,000 records

| Operation | SQLite p50 / p95 ms | Pebble p50 / p95 ms |
|---|---:|---:|
| Primary-key get | 0.224 / 0.711 | 0.105 / 0.249 |
| Unique email find | 0.145 / 0.605 | 0.128 / 0.254 |
| Compound-index page (50) | 0.514 / 1.159 | 1.813 / 3.123 |
| Cursor page 2 (50) | 0.565 / 1.125 | 1.351 / 2.058 |
| Aggregate one tenant | 0.176 / 0.282 | 2.422 / 3.176 |
| Aggregate whole collection | 82.235 / 555.433 | Rejected: query_too_expensive |
| Count whole collection | 3.064 / 3.722 | Rejected: query_too_expensive |
| Unindexed find | 32.663 / 43.472 | Rejected: query_too_expensive |
| Update indexed timestamp | 0.221 / 0.388 | 5.033 / 9.126 |
| Insert one record | 0.191 / 0.393 | 4.993 / 9.588 |
| Get, eight clients | 0.389 / 1.556 | 0.186 / 1.355 |
| Create index after loading | Rejected: resource_limit | Rejected: resource_limit |

### Throughput

Median of per-trial phase throughput; loop/check overhead is included. Bulk loading also includes synthetic record construction. Writes maintain all three secondary indexes.

| Records | Adapter | Bulk records/sec (1,000 per transaction) | Gets/sec, eight clients | Single inserts/sec |
|---:|---|---:|---:|---:|
| 10,000 | sqlite | 13,631 | 13,836 | 2,043 |
| 10,000 | pebble | 8,257 | 28,407 | 165 |
| 100,000 | sqlite | 9,809 | 13,712 | 2,899 |
| 100,000 | pebble | 6,205 | 32,609 | 189 |

## Million-record results

Only completed trials appear in timing tables below; the load failures and interruption above remain part of the outcome. Each column is one trial, not a pooled estimate.

| Operation | pebble run 1 (before restart) | pebble run 3 (after restart) | sqlite run 2 (after restart) | sqlite run 3 (after restart) |
|---|---:|---:|---:|---:|
| Primary-key get | 0.890 / 2.875 | 0.294 / 0.821 | 0.636 / 1.255 | 0.438 / 2.245 |
| Unique email find | 0.696 / 2.662 | 0.123 / 0.402 | 0.327 / 0.521 | 0.292 / 1.527 |
| Compound-index page (50) | 1.856 / 4.998 | 1.138 / 1.893 | 0.645 / 1.403 | 0.542 / 1.011 |
| Cursor page 2 (50) | 2.446 / 5.500 | 1.179 / 1.822 | 0.695 / 1.384 | 0.571 / 1.020 |
| Aggregate one tenant | 807.642 / 1344.239 | 17.592 / 19.226 | 5.026 / 6.496 | 3.277 / 3.760 |
| Aggregate whole collection | Rejected: query_too_expensive | Rejected: query_too_expensive | Timed out: deadline_exceeded | 1039.153 / 1098.267 (3/7 succeeded) |
| Count whole collection | Rejected: query_too_expensive | Rejected: query_too_expensive | 29.796 / 488.503 | 28.936 / 30.571 |
| Unindexed find | Rejected: query_too_expensive | Rejected: query_too_expensive | 552.979 / 1515.271 | 302.790 / 305.788 |
| Update indexed timestamp | 5.760 / 9.903 | 5.311 / 8.341 | 0.451 / 1.553 | 0.291 / 5.945 |
| Insert one record | 5.437 / 12.420 | 5.427 / 9.689 | 0.191 / 0.305 | 0.317 / 6.140 |
| Get, eight clients | 0.911 / 2.052 | 0.235 / 0.529 | 0.351 / 1.420 | 0.347 / 1.347 |

Values are p50 / p95 milliseconds. The filtered aggregate processes 1,000 records at this scale; the full aggregate attempts all one million. SQLite full aggregates succeeded on 3 of 14 attempts; 11 timed out.

Pebble’s filtered-aggregate median varied from 807.6 ms before the restart to 17.6 ms afterward with the same binary. Host/cache conditions are plausible contributors, but this experiment does not isolate the cause. The separate trial columns retain that variability.

| Trial | Bulk records/sec | Completed-trial duration |
|---|---:|---:|
| pebble run 1 (before restart) | 4,669 | 232.1 s |
| pebble run 3 (after restart) | 4,113 | 250.2 s |
| sqlite run 2 (after restart) | 4,151 | 268.5 s |
| sqlite run 3 (after restart) | 5,853 | 188.2 s |

## Storage and process memory

For 10k/100k: median logical file sizes and range of per-process peak RSS across three trials. Million-record rows are individual completed trials. Measurements include the registry; disk is after clean close without forced compaction. Peak RSS covers the entire process, including import.

| Dataset / trial | Adapter | Closed disk MiB | Peak RSS MiB |
|---|---|---:|---:|
| 10,000 records, 3 trials | sqlite | 10.5 | 54.2–56.8 |
| 10,000 records, 3 trials | pebble | 4.4 | 47.0–53.0 |
| 100,000 records, 3 trials | sqlite | 102.6 | 53.4–56.3 |
| 100,000 records, 3 trials | pebble | 16.4 | 62.7–181.4 |
| 1m, run 1, before restart | pebble | 148.8 | 62.5 |
| 1m, run 3, after restart | pebble | 148.9 | 155.7 |
| 1m, run 2, after restart | sqlite | 1054.8 | 48.9 |
| 1m, run 3, after restart | sqlite | 1054.8 | 53.3 |

The dataset contains a repeated 112-character note suffix and patterned values, making it compressible. Storage ratios will change with real data. Allocation deltas and heap-after-phase values are available in the JSON/CSV; they include benchmark-loop allocations and are not retained-memory measurements.

## What the tests establish

- Both adapters execute the same indexed find/get, pagination, and aggregate contract. Explain plans confirm native use of the email and status/timestamp indexes for the measured queries.
- Result checks cover returned IDs, filtering/order, page boundaries, group counts/sums/averages, and affected-record counts. They do not establish crash durability or exhaustive correctness.
- At the time of these trials, Pebble rejected full-collection count, aggregate, and unindexed search above 10,000 candidates. That scan cap has since been removed; see [large-scan verification](LARGE-SCANS.md). The historical timings and errors below are preserved.
- Both adapters reject synchronous index creation on collections above 10,000 existing records. The large benchmark imports created indexes before loading to avoid that restriction.
- Full-collection SQLite aggregation at one million records exceeded the deadline in the post-restart trials; the operation tables retain any partial success counts. Large reporting queries need separate capacity and deadline planning.
- A legal 1,000-record insert batch can exceed the two-second request deadline under observed host conditions. The current limits do not guarantee reliable completion of large imports.

## Method and limits

Apple M1 Pro, 10 logical CPUs, 16 GiB RAM; macOS 26.5.2; Go 1.25.1 darwin/arm64; CGO disabled. Trials ran serially on the user’s normal laptop, not a dedicated benchmark host. The machine restarted during the experiment. No production data was touched.

One logical database and one collection per trial, one primary index plus three secondary indexes, deterministic records, and fresh temporary storage. The measured call includes engine.Manager.Execute and JSON result encoding, with the existing two-second deadline and safety limits. It excludes HTTP/MCP transport, authentication, input JSON decoding, UI rendering, and cloud access. Caches are not flushed; no cold-cache claim is made. SQLite WAL/FULL and Pebble synchronous writes remain as shipped.

This is an initial performance experiment, not a production capacity qualification. It does not test many simultaneously active databases/collections, mixed read/write load, long-running compaction, controlled crash recovery, or sustained p99 latency.

## Follow-up work

1. Keep SQLite as the default for the current general-purpose workload.
2. Add resumable imports with explicit progress and suitable batch/deadline handling, then retest on an otherwise idle host.
3. Profile Pebble aggregation and its large run-to-run variation. For write throughput, batch durable commits and first align adapter durability settings; see the diagnosis above.
4. Add background index builds before promising index creation on large existing collections.
5. Add end-to-end and mixed-workload measurements before making service-level latency or capacity promises.

## Validation of benchmark changes

`GOTOOLCHAIN=local GOWORK=off go test -p 2 ./...` and `go vet -p 2 ./...` passed after the benchmark matrix. Both Python scripts parse successfully. The 16 JSON reports have unique trial IDs and internally consistent sample counts. Production engine code and limits were not changed for this performance exercise.

## Reproduce and inspect

- [Runner instructions](README.md)
- [Pre-restart machine metadata](results/2026-09-07/machine.json)
- [Pre-restart operation summary](results/2026-09-07/operations.csv)
- [Interruption record](results/2026-09-07/interruption.json)
- [Post-restart machine metadata](results/2026-09-07-post-restart/machine.json)
- [Post-restart operation summary](results/2026-09-07-post-restart/operations.csv)
- Per-request samples, plans, and logs are beside those summaries. Failed population attempts have a log and no completed report.
