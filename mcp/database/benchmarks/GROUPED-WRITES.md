# Pebble durable write batching — 7 September 2026

Implemented automatic grouping of concurrent record-write requests. Validation and index maintenance remain serialized per database. Each request has its own rollback boundary, while successful requests share a synchronous Pebble commit. Every successful response still waits for the disk flush. SQLite keeps its existing write path.

## Measured improvement

Same-process controlled comparison of the original serial transaction path and the new grouped path, using the same engine code, data, three secondary indexes, and `pebble.Sync` durability. The serial control disables only the optional grouped-backend interface. Three repetitions per mode and concurrency level, reversing mode order in the middle repetition. Throughput is the median of trial throughputs; latency percentiles pool successful samples.

| Concurrent writers | Before inserts/sec | After inserts/sec | Improvement | Before p50 / p95 ms | After p50 / p95 ms |
|---:|---:|---:|---:|---:|---:|
| 1 | 233 | 234 | 1.0× | 4.05 / 5.48 | 4.13 / 6.33 |
| 8 | 214 | 856 | 4.0× | 35.97 / 46.88 | 9.01 / 12.76 |
| 32 | 245 | 3,022 | 12.4× | 131.92 / 157.22 | 10.21 / 12.75 |

At 32 writers, 3,840 single inserts needed 3,840 durable commits through the original path and **240 commits** through the grouped path. The gain comes from sharing the flush cost. A lone writer remains around 4 ms because it still waits for its own durable flush. No artificial batching delay is added.

## Behavior and bounds

- Applies automatically to Pebble `insert`, `update`, `delete`, `upsert`, and `batch` requests within one database. Independent databases have independent queues; collections within one database can share a commit.
- A transient worker drains requests already waiting. It exits when idle. Up to 64 requests may wait behind the active group; overflow returns `resource_limit`.
- A candidate batch contains earlier successful writes and the current request. Rejecting the current request discards its entire candidate, preserving prior accepted writes. This keeps uniqueness checks, version checks, and cross-collection batch rollback intact.
- The combined encoded batch is flushed after reaching 1 MiB. A single API transaction can exceed that grouping threshold and is committed intact; it is never split.
- Reads, schema changes, drop, and close remain coordinated by the database lock. Data is not exposed to reads before the group commit finishes.
- Cancellation before staging skips the request. Once staged, the caller waits for the definitive commit result. A storage-sync error fails the affected requests and blocks subsequent data access on that handle until the app is reopened.
- The implementation uses ordinary indexed batches and synchronous commits, without Pebble’s experimental asynchronous-commit API.

## Validation

The full app/engine suite passes under the race detector; `go vet` passes. New tests exercise successful members around a failed request, rollback of partially staged records and unique index keys, read-your-previous-write inside the group, persistence after reopening, cancellation, commit error propagation, concurrent increments, the write-queue bound, the byte threshold, and response/read/close/drop barriers around a blocked commit. Existing tests cover project/database isolation, cross-collection atomic batches, composite keys, indexes, pagination, and HTTP/MCP integration.

All 18 benchmark trials completed, all 9,840 inserts returned the expected affected count, and final collection counts matched. This is a short local throughput experiment, not a crash-recovery or endurance qualification.

## Method and reproduction

Apple M1 Pro, 16 GiB RAM, macOS 26.5.2, Go 1.25.1, CGO disabled. Each trial starts with 1,004 records and three secondary indexes. Each client makes 40 sequential single-record insert calls. Timings include manager routing and result JSON encoding, excluding HTTP, authentication, and record generation. Normal writes retain the stronger macOS flush behavior diagnosed earlier.

Run from the Database app directory with a new output path:

```sh
GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 \
DB_WRITE_BENCHMARK_OUTPUT=/tmp/pebble-grouped-writes-new.json \
go test -p 2 -tags writebenchmark \
  -run '^TestConcurrentWriteBenchmark$' -count=1 -v ./engine
```

- [Benchmark source](../engine/write_benchmark_test.go)
- [Raw samples and durable commit counts](results/grouped-writes-2026-09-07.json)
- [Source hashes](results/grouped-writes-2026-09-07-source.json)
- [Original durability diagnosis](INSERT-DURABILITY.md)
