# Single-insert durability diagnosis — 7 September 2026

The roughly 5 ms Pebble single-insert latency is dominated by synchronous commit. The original as-shipped write comparison used different macOS flush strengths: Pebble reaches Go `os.File.Sync`, which issues `F_FULLFSYNC`; SQLite uses `synchronous=FULL` but leaves its separate `fullfsync` flag off. The difference was confirmed by querying the actual SQLite connection, reading the installed dependency/runtime code, and running a controlled diagnostic.

## Controlled measurement

Same M1 Pro host, Go 1.25.1, CGO disabled. Three trials per mode, 200 sequential single inserts per trial, fresh temporary database, 1,000 seeded records and three secondary indexes. The order was reversed in the middle repetition. Timings include the public engine contract and result JSON encoding, excluding row construction. No production data or application settings were changed.

Pooled nearest-rank latency over 600 inserts per mode:

| Mode | p50 ms | p95 ms |
|---|---:|---:|
| SQLite as shipped: WAL/FULL, fullfsync=OFF | 0.209 | 0.356 |
| SQLite: WAL/FULL, fullfsync=ON | 4.473 | 6.913 |
| Pebble as shipped: Commit(Sync) | 4.064 | 6.085 |

Instrumenting Pebble's transaction path measured 0.068 ms median to prepare the batch, including record/index handling, and 3.913 ms median inside `batch.Commit(pebble.Sync)`. Commit accounted for **94.7% of summed end-to-end insert time**. That region includes Pebble commit coordination and log synchronization; this is not a syscall-only disk trace.

Enabling SQLite's stronger flush closes the observed single-write gap. This experiment isolates a major cause of the local result, rather than establishing equivalent durability under every crash scenario or performance on other operating systems. It uses a smaller dataset than the original 100k benchmark, so its absolute timings belong to this diagnostic.

## Implications

- The original write timings are valid for the current settings, but must not be interpreted as equivalent-durability engine comparisons.
- SQLite's indexed-query and aggregation measurements are separate findings; this diagnostic does not rerun or replace them.
- Batch multiple records into one durable transaction to share the flush cost. The existing insert API already accepts arrays of records.
- Follow-up: [automatic durable group commits are now implemented](GROUPED-WRITES.md). Validation stays serialized, while concurrent requests can share a flush.
- Define and verify common durability semantics for adapters before publishing comparative write-throughput claims. Do not silently disable Pebble synchronization to improve benchmark numbers.

## Reproduction

From the Database app directory, choose a new absolute output path:

```sh
GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 \
DB_INSERT_DIAGNOSTIC_OUTPUT=/tmp/insert-durability-new.json \
go test -p 2 -tags insertdiagnostic \
  -run '^TestInsertDurabilityDiagnostic$' -count=1 -v ./engine
```

The diagnostic is excluded from ordinary builds/tests unless explicitly selected with the build tag. SQLite fullfsync changes apply only to its temporary test connection. The Pebble probe preserves the original synchronous commit behavior. All nine diagnostic cases passed.

- [Diagnostic source](../engine/insert_diagnostic_test.go)
- [Raw samples and per-insert phase timings](results/insert-durability-2026-09-07.json)
- [Go's macOS sync implementation](https://go.dev/src/internal/poll/fd_fsync_darwin.go)
- [SQLite fullfsync documentation](https://www.sqlite.org/pragma.html#pragma_fullfsync)
- [SQLite synchronization flags versus synchronous pragma](https://sqlite.org/c3ref/c_sync_dataonly.html)
