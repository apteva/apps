# SQLite vs Pebble — complete comparison, 7 September 2026

🟢 marks the lower measured median latency, higher throughput, or smaller disk size. It is not a claim of statistical significance. Failed/rejected operations are not ranked as faster.

For the latest broad-query performance after removing the scan cap and optimizing Pebble, see [the million-record scan comparison](OPTIMIZED-SCANS.md). The earlier scale table below retains its original measurements.

## Earlier scale benchmark

Median milliseconds; lower is better. These scale results predate Pebble write grouping. The 10k/100k columns pool three runs; 1m uses the last completed post-restart trial for each adapter. The updated write comparison follows.

| Operation | SQLite 10k | Pebble 10k | SQLite 100k | Pebble 100k | SQLite 1m | Pebble 1m |
|---|---:|---:|---:|---:|---:|---:|
| Primary-key lookup | 0.059 | 🟢 **0.055** | 0.224 | 🟢 **0.105** | 0.438 | 🟢 **0.294** |
| Unique email lookup | 🟢 **0.075** | 0.087 | 0.145 | 🟢 **0.128** | 0.292 | 🟢 **0.123** |
| Indexed page, 50 records | 🟢 **0.530** | 1.397 | 🟢 **0.514** | 1.813 | 🟢 **0.542** | 1.138 |
| Cursor page 2, 50 records | 🟢 **0.555** | 1.435 | 🟢 **0.565** | 1.351 | 🟢 **0.571** | 1.179 |
| Aggregate one tenant | 🟢 **0.098** | 0.298 | 🟢 **0.176** | 2.422 | 🟢 **3.277** | 17.592 |
| Aggregate whole collection | 🟢 **7.159** | 230.501 | 82.235 | Limit | Timeouts | Limit |
| Count all | 🟢 **0.225** | 225.500 | 3.064 | Limit | 28.936 | Limit |
| Unindexed find | 🟢 **3.433** | 227.517 | 32.663 | Limit | 302.790 | Limit |
| Update indexed record† | 🟢 **0.274** | 6.889 | 🟢 **0.221** | 5.033 | 🟢 **0.291** | 5.311 |
| Single insert† | 🟢 **0.300** | 5.137 | 🟢 **0.191** | 4.993 | 🟢 **0.317** | 5.427 |
| Read with 8 clients | 0.389 | 🟢 **0.214** | 0.389 | 🟢 **0.186** | 0.347 | 🟢 **0.235** |
| Build index after loading† | 🟢 **70.342** | 86.816 | Limit | Limit | Limit | Limit |
| Bulk inserts/sec† | 🟢 **13,631** | 8,257 | 🟢 **9,809** | 6,205 | 🟢 **5,853** | 4,113 |
| Reads/sec, 8 clients | 13,836 | 🟢 **28,407** | 13,712 | 🟢 **32,609** | 15,722 | 🟢 **28,950** |

† These are app-default write measurements with different macOS flush strengths. SQLite uses fullfsync=OFF; Pebble uses the stronger disk flush. Do not interpret those rows as an equivalent-durability comparison.

These historical trials used Pebble's former 10k-candidate scan limit and a two-second query deadline. The candidate cap has since been removed and broad reads now default to 30 seconds; see [large-scan verification](LARGE-SCANS.md) for current behavior. Both adapters still reject synchronous index builds over 10k existing records. In the historical trials, SQLite whole-collection aggregation at 1m timed out on 11 of 14 attempts across the two completed trials. Two import attempts timed out; a computer-crash interruption was separately rerun. Failed attempts are retained in the original reports.

## New write benchmark with current application defaults

Fresh measurement of the updated code. Cells show median milliseconds / median inserts per second. SQLite uses its current weaker macOS flush; Pebble retains the stronger flush.

| Concurrent writers | SQLite as configured | Updated Pebble |
|---:|---:|---:|
| 1 | 🟢 **0.22 ms / 4,512/s** | 3.99 ms / 247/s |
| 8 | 🟢 **1.56 ms / 4,280/s** | 9.41 ms / 787/s |
| 32 | 🟢 **6.26 ms / 4,808/s** | 11.31 ms / 2,356/s |

## New write benchmark with matched strong macOS flush

SQLite fullfsync=ON applies only to this temporary benchmark connection. It is not an app configuration change.

| Concurrent writers | SQLite, stronger flush | Updated Pebble |
|---:|---:|---:|
| 1 | 4.95 ms / 199/s | 🟢 **3.99 ms / 247/s** |
| 8 | 39.62 ms / 196/s | 🟢 **9.41 ms / 787/s** |
| 32 | 157.72 ms / 203/s | 🟢 **11.31 ms / 2,356/s** |

All 27 new write trials passed, including affected-record checks and final collection counts. Same M1 Pro host, Go 1.25.1, CGO disabled; 1,004 seeded records, three secondary indexes, 40 inserts per client, three runs per configuration/concurrency level. Mode order reversed in the middle repetition. Latency percentiles pool samples; throughput is the median of trial rates. Measurements include the engine contract and result JSON encoding, excluding HTTP and UI.

## Disk footprint

MiB after clean close. Green means smaller. 10k/100k are medians of three earlier trials; 1m uses the last completed post-restart trial. Synthetic data is repetitive and compressible.

| Records | SQLite | Pebble |
|---:|---:|---:|
| 10k | 10.5 | 🟢 **4.4** |
| 100k | 102.6 | 🟢 **16.4** |
| 1m | 1,054.8 | 🟢 **148.9** |

## Reproduce the new write comparison

```sh
GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 \
DB_WRITE_BENCHMARK_COMPARE=1 \
DB_WRITE_BENCHMARK_OUTPUT=/tmp/sqlite-pebble-writes-new.json \
go test -p 2 -tags writebenchmark \
  -run '^TestConcurrentWriteBenchmark$' -count=1 -v ./engine
```

- [Original scale report](RESULTS.md)
- [Write batching implementation and before/after benchmark](GROUPED-WRITES.md)
- [New write samples](results/sqlite-pebble-writes-2026-09-07.json)
- [New write summary](results/sqlite-pebble-writes-2026-09-07-summary.json)
