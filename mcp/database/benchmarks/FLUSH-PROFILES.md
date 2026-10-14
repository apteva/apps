# Matched macOS write flush profiles

14 October 2026. Local branch `fix/database-native-performance`, Database 0.4.4 development build, AptevaDB `v0.2.0-alpha.4`, app-sdk v0.82.0 (HEAD tag rechecked). The source and standalone sidecar build are local and unpublished. Installed application data/settings were not changed.

**The adapter now exposes AptevaDB's existing flush choice, preserving synchronous writes and the full-flush default.** A fresh comparison confirms that the previous single-insert gap was mainly flush policy: with ordinary fsync requested by both, AptevaDB is faster than SQLite in these samples.

## Configuration

Set app configuration `aptevadb_sync_mode` or environment `DATABASE_APTEVADB_SYNC_MODE` to:

| Value | Native option | macOS behavior |
|---|---|---|
| `full` (default) | Sync=true, SyncFull | Full drive-cache flush |
| `os` | Sync=true, SyncOS | Ordinary fsync, like SQLite fullfsync=OFF |
| `barrier` | Sync=true, SyncBarrier | Orders writes without forcing the drive cache to stable media |

App configuration overrides environment. The selected value applies on mount; restart/remount to apply a change to all owned native database handles. No exposed choice enables asynchronous writes. Invalid values fail before opening a database. The effective choice and `synchronousWrites: true` appear in `db_explain`, and the choice is logged at mount. Profile changes preserve the native storage format, schema and record API.

OS/barrier give weaker power-loss protection on macOS. On other supported platforms these modes use fsync; the latency differences reported here are specific to the macOS primitives. Matching the requested primitive does not establish identical recovery behavior across different engine/WAL designs.

```sh
DATABASE_APTEVADB_SYNC_MODE=os
```

## Ordinary fsync comparison

Each cell is **adapter / direct driver**, with units in the workload. 100,000 seeded records; three fresh database fixtures per mode/backend in one benchmark process. Adapter order rotates, and app/direct order reverses in the middle repetition. Native references use the same codec, primary index, record metadata, atomic batch size and response shape. Timings include response JSON encoding. API routing/validation are present in the adapter measurements.

AptevaDB uses Sync=true / SyncOS. Both SQLite writer connections are reserved and queried to confirm `synchronous=2` (FULL), `fullfsync=0`. Pebble is omitted here because its shipped synchronous profile requests a full macOS flush.

| Workload | AptevaDB | SQLite |
|---|---:|---:|
| Point read, µs | 4.916 / 4.583 | 11.167 / 10.083 |
| Row count, ms | 3.665 / 3.235 | 8.742 / 7.148 |
| Numeric SUM, ms | 3.477 / 3.324 | 12.935 / 13.208 |
| Single insert, ms | 0.052 / 0.043 | 0.080 / 0.082 |
| 1,000-row insert, ms | 4.168 / 3.331 | 4.941 / 4.383 |

These results remove the apparent 50× single-write disadvantage from the earlier mixed-profile table. Direct AptevaDB and adapter results improve together, supporting flush cost as the cause. The record/index operations and API validation remain enabled.

## Full-flush comparison

Fresh fixtures and the same harness. AptevaDB uses Sync=true / SyncFull; SQLite verifies `synchronous=2` and `fullfsync=1` on both writer connections; Pebble uses its shipped synchronous commit/full-flush path. SQLite's stronger setting is diagnostic and does not change the application's default SQLite profile. Its setting is enabled after seeding, before every measured write; initial seed-loading times are not compared.

| Workload | AptevaDB | SQLite | Pebble |
|---|---:|---:|---:|
| Point read, µs | 5.333 / 4.417 | 10.583 / 9.791 | 14.625 / 13.958 |
| Row count, ms | 3.534 / 3.018 | 8.426 / 7.275 | 20.277 / 19.911 |
| Numeric SUM, ms | 3.067 / 2.715 | 12.831 / 12.862 | 40.240 / 45.233 |
| Single insert, ms | 4.010 / 3.989 | 4.113 / 4.180 | 4.069 / 4.006 |
| 1,000-row insert, ms | 8.328 / 8.033 | 9.948 / 9.292 | 26.165 / 29.016 |

The single-insert results are comparable when each engine requests a full flush. Batch costs also include record encoding, index handling, WAL/b-tree layout and coordination, so equal requested flush strength does not imply equal bulk latency.

## Checks and evidence

- Full app/backend tests and full race suite passed; vet with the aggregate benchmark harness passed.
- All three exposed native modes preserve Sync=true and checksum verification. Each mode writes an exact integer, closes and reopens under full flush, then reads/updates/aggregates it successfully. Zero/omitted options select full flush; invalid modes are rejected.
- A compiled sidecar received `APTEVA_APP_CONFIG` selecting OS and an environment value selecting full. Its HTTP `explain` reports OS and synchronous writes, proving mounted app config precedence through the real SDK path. Existing HTTP/MCP scope/authentication and three-backend operations pass.
- All 30 fresh driver fixtures passed: 12 ordinary-profile fixtures and 18 full-profile fixtures. Timed reads/counts/sums are validated against expected values. Writes check affected counts and every returned key; sampled first/last batch records and total post-write cardinality are verified after timing.
- Each fixture performs 1,000 timed point reads, 15 counts, 15 sums, 100 single inserts and 20 thousand-record batches. Every latency sample, per-trial median and tail is retained.

Fixtures use fixed-width IDs, a numeric amount, 128-byte note and service metadata, with one primary unique index and no secondary indexes. These driver results are distinct from the earlier million-row grouped schema. Timing processes ran sequentially after compile/test/race work finished. Hardware: Apple M1 Pro, macOS arm64, 10 logical CPUs, 16 GiB RAM, Go 1.25.1, CGO disabled. Background desktop activity was present. This is a local warmed-fixture comparison, not a cold-storage or production capacity guarantee.

[Ordinary raw samples](results/database-flush-profiles-2026-10-14/ordinary.json) · [Full-flush raw samples](results/database-flush-profiles-2026-10-14/full.json) · [All medians/ranges/trial values](results/database-flush-profiles-2026-10-14/summary.json) · [Run order, hardware and binary hash](results/database-flush-profiles-2026-10-14/conditions.json).

## Reproduction

From the app directory, choose a fresh output file:

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
 NATIVE_COMPARISON_ROWS=100000 NATIVE_COMPARISON_ADAPTERS=sqlite,aptevadb \
 NATIVE_COMPARISON_APTEVADB_SYNC_MODE=os NATIVE_COMPARISON_SQLITE_FULLFSYNC=0 \
 NATIVE_COMPARISON_OUTPUT=/absolute/path/to/new-ordinary.json \
 go test ./engine -run '^TestNativeDriverComparison$' -v -count=1 -timeout=20m
```

For full flushes, select `NATIVE_COMPARISON_ADAPTERS=sqlite,pebble,aptevadb`, `NATIVE_COMPARISON_APTEVADB_SYNC_MODE=full` and `NATIVE_COMPARISON_SQLITE_FULLFSYNC=1`, with a different output file. The [saved sequential runner](results/database-flush-profiles-2026-10-14/run.py) uses the precompiled benchmark binary. `DB_HUGE_AGG_SYNC_MODE` provides the same selection for the optional million-row aggregate harness; those read workloads were not rerun because this change selects the write flush primitive and leaves their query implementations unchanged.

Source/binary checksums, logs and the standalone sidecar are in [the result directory](results/database-flush-profiles-2026-10-14). Existing installed data was not opened or upgraded.
