# AptevaDB backend implementation and validation

13 October 2026. Unreleased development on `fix/database-native-performance`, based on Database `database/v0.4.4` / `a29c1263e3d4f8ac52ae6afe859f72812cd186c4`.

**Implemented the optional `aptevadb` backend using `github.com/apteva/db v0.2.0-alpha.3`. The full contract, compiled sidecar, race checks and three-backend benchmarks pass.** SQLite remains the default. The source changes and local binary are ready; the running installed Database 0.4.4 has not been replaced and this work has not been published.

Create a new database through MCP or HTTP using:

```json
{"database":"native","adapter":"aptevadb"}
```

The dashboard selector includes **AptevaDB (experimental)**. Existing databases retain their selected adapter. Export/import migration and opening a standalone native directory are not implemented.

## Implementation

- Open each managed native directory once with `DurableOptions()` and close it during manager shutdown. Only one process owns a directory. The SDK/control registry remains SQLite.
- Typed binary documents preserve integer wire strings as native int64, numeric fields as float64, and service metadata. JSON fields store opaque canonical JSON so exact nested numbers, deep JSON and large documents retain the shared API contract.
- Typed physical primary keys make native secondary-index suffixes supply the stable ascending primary tie-breaker. Compound/mixed-direction indexes use the engine directly; nullable uniqueness uses `NullsDistinct: true`.
- Native atomic batches, a consistent snapshot and an indexed write overlay implement read-your-writes, cross-collection rollback, version checks and patch upserts. Preflight uniqueness rejects temporary conflicts inside a batch, consistently with SQLite/Pebble.
- A durable schema intent is logged before native collection/index DDL. Final schema publication and intent deletion commit atomically. Mount recovery replays an interrupted operation idempotently. Uncertain rollback/publication blocks subsequent access until reopen.
- Indexed reads stream, projected sorts retain a bounded top page, and compatible grouped aggregates use the native streaming pipeline without per-record map decoding. Integer sums/averages and nullable field counts retain the portable checked accumulator. Retained query/group state has the same 16 MiB accounting budget.
- Scoped routing/schema caches and direct point-read paths remain enabled for SQLite/Pebble. The shared aggregate helper retains Pebble's streaming decoder and typed accumulation behavior.

The shared app limits still apply, including 1 MiB records, 2,048-byte canonical primary keys, 4,096-byte index tuples, 1,000-record pages/writes and a 10,000-record synchronous index-build limit. Native alpha.3's larger engine limits do not widen the portable API automatically. New native directories use format 8. No existing native data was opened or migrated.

## Validation

| Check | Result |
|---|---|
| Full `GOWORK=off GOTOOLCHAIN=local go test ./...` | PASS |
| Full `go test -race ./...` | PASS |
| `go vet ./...` | PASS |
| Compiled sidecar HTTP and MCP/auth/isolation checks across all three backends | PASS |
| SQLite v1 migration and warmed point-read regression | PASS |
| Differential filters, exact integer/composite keys, null ordering and signed pagination | PASS |
| Batch read-your-writes, temporary unique conflicts, cross-collection rollback | PASS |
| Durable schema recovery before/after physical create/index/drop, repeated reopen | PASS |
| Exact maximum primary/index tuple, NUL escaping, 80 KB and deep JSON round trips | PASS |
| Projected page over >16 MiB input and aggregate memory/overflow bounds | PASS |
| Bun panel build and host React import verification | PASS |
| Fresh 18-trial indexed benchmark matrix | PASS; 421,254 correctness checks |
| Paired reads and direct-driver write/count/sum comparison | PASS |
| Full-flush SQLite diagnostic on both writer connections | PASS |

All indexed-matrix operations succeeded. All 27 late index builds at 10k records succeeded. All 27 attempts at 100k records returned the expected `resource_limit` due to the shared 10k build limit. No failed request is included as successful latency.

The SDK pin was rechecked by fetched tags and HEAD topology: `v0.82.0` at `950b91d` remains the current SDK HEAD tag.

## Point-read overhead against the native drivers

100k records; three independent fixtures per adapter. On each warmed fixture, 2,000 measured app/direct pairs alternate execution order. Serialized responses must be identical. Input construction and result JSON encoding are timed; authentication/HTTP/input JSON decoding are excluded. Values are the median of three trial p50s, with the range of those p50s in parentheses, in **µs**.

| Backend | App engine | Direct driver | Added median µs |
|---|---:|---:|---:|
| sqlite | 10.12 (9.50–10.12) | 8.88 (8.17–8.96) | 1.25 |
| pebble | 14.42 (14.17–14.46) | 13.67 (13.46–13.83) | 0.75 |
| aptevadb | 4.38 (4.33–4.54) | 3.75 (3.67–3.92) | 0.62 |

SQLite/Pebble point reads are close to their driver floor in these paired checks, with approximately one microsecond or less of extra routing/validation work. There is measurable overhead; this is not a claim of identical performance for every operation.

## Full app comparison with three secondary indexes

Fresh temporary directories, 10k and 100k records, three repetitions each, rotating backend order. Records have typed IDs/email/tenant/status/timestamp/amount/units/note plus service metadata, a primary index and three secondary indexes. These **100k-record results are in ms**, median of per-trial p50s with min–max trial p50s. Full 10k results and individual/tail samples are in the linked raw reports.

| Operation | SQLite | Pebble | AptevaDB |
|---|---:|---:|---:|
| Unique lookup | 0.059 (0.057–0.065) | 0.090 (0.087–0.102) | 0.025 (0.025–0.029) |
| Indexed ordered page, 50 rows | 0.498 (0.485–0.507) | 1.718 (1.650–2.389) | 0.354 (0.351–0.451) |
| Second page, 50 rows | 0.565 (0.562–0.565) | 0.742 (0.731–0.815) | 0.431 (0.404–0.443) |
| Filtered grouped aggregate | 0.168 (0.163–0.180) | 1.039 (0.662–1.187) | 0.097 (0.094–0.117) |
| Full grouped aggregate | 91.617 (88.177–106.293) | 76.367 (72.478–124.509) | 13.078 (11.837–15.016) |
| Unfiltered count | 3.335 (2.882–3.379) | 32.344 (30.412–36.342) | 5.676 (5.135–7.920) |
| Unindexed last-record search | 17.110 (16.859–17.982) | 91.986 (85.243–121.868) | 61.376 (60.430–61.820) |
| Eight-client point reads | 0.074 (0.074–0.075) | 0.032 (0.031–0.033) | 0.009 (0.009–0.010) |
| Load batch, 1,000 records | 25.462 (23.059–27.020) | 124.173 (109.290–139.189) | 14.481 (14.347–15.378) |
| Single insert | 0.114 (0.105–0.118) | 5.404 (4.982–5.829) | 5.098 (4.011–6.130) |

AptevaDB led indexed pages and grouped aggregates here. SQLite led unfiltered counts. Storage/cache implementations and record encodings differ. The default SQLite write profile also uses weaker macOS flush semantics than the other two, so the default single-write column is not an equal-durability ranking.

## App/direct comparison and full-flush writes

The simpler comparison schema has one primary index and no secondary indexes. Direct SQLite uses prepared statements; direct Pebble maintains the app's primary/unique-owner keys; direct AptevaDB uses the same typed document codec, primary unique index and native atomic batches. Native AptevaDB sums use a compiled native streaming pipeline. Results include serialized outputs and are checked after timing.

Values below are **ms**, median of three trial p50s with the range of trial p50s. SQLite writes come from the separate `fullfsync=ON` diagnostic so all three request full macOS flushes. SQLite reads retain the default WAL/FULL read profile. The original default-write results are preserved separately in `direct-drivers.json`.

| Backend | Operation | App engine | Direct driver |
|---|---|---:|---:|
| sqlite | count | 11.39 (9.26–11.49) | 8.16 (7.47–13.18) |
| sqlite | sum | 14.96 (13.84–16.81) | 13.90 (13.46–17.02) |
| sqlite | insert_1 | 4.70 (4.04–4.90) | 4.79 (4.16–4.86) |
| sqlite | insert_1000 | 10.10 (10.05–11.08) | 8.94 (8.89–9.91) |
| pebble | count | 21.26 (20.28–109.90) | 26.29 (19.51–33.21) |
| pebble | sum | 44.21 (43.64–64.34) | 46.47 (45.65–47.03) |
| pebble | insert_1 | 4.96 (3.99–12.57) | 4.04 (4.03–5.01) |
| pebble | insert_1000 | 29.04 (27.20–37.05) | 33.90 (27.49–35.08) |
| aptevadb | count | 4.17 (4.15–4.20) | 3.60 (3.52–3.79) |
| aptevadb | sum | 12.92 (11.74–13.00) | 12.61 (12.29–13.18) |
| aptevadb | insert_1 | 3.99 (3.99–6.04) | 4.00 (4.00–4.02) |
| aptevadb | insert_1000 | 9.17 (8.03–11.26) | 7.96 (7.95–8.62) |

The shipped SQLite profile uses WAL, `synchronous=FULL`, `fullfsync=OFF`. Pebble uses synchronous commits and AptevaDB uses `DurableOptions()` / `SyncFull`. The diagnostic explicitly reserves and checks both SQLite writer connections for `fullfsync=ON`; it does not change the app's default. Matching requested flush strength still does not make all engine/WAL/cache layouts identical.

The initial native aggregate implementation decoded every input document. Its 100k sum was roughly 111 ms in the first run. Moving compatible aggregates to the native pipeline brought the measured median to 12.92 ms, versus 12.61 ms for the direct native pipeline. Before-change measurements are retained for traceability, not pooled with final results.

## Remaining native-engine improvement

**Native integer aggregate overflow still wraps in alpha.3.** A separate disposable probe summed two int64 maximum values and returned `-2` without an error. The app correctly returns `resource_limit` using its checked portable path; the API is protected. Native checked integer accumulation would let those aggregates use the fast native path too. Nullable field counts also use the portable path because native `$count` counts all rows.

Reproduce the upstream overflow witness:

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 go run <local-benchmark-results>/database-three-backends-2026-10-13/integer-overflow.go
```

## Measurement limits and artifacts

Apple M1 Pro, 10 logical CPUs, 16 GiB RAM, macOS arm64, Go 1.25.1, `CGO_ENABLED=0`. The machine was active and other independent database tests/benchmarks ran concurrently. Large p95/range variation is visible, especially in some Pebble trials. Paired point reads reduce between-fixture cache/load skew. Treat these as local smoke measurements, not production capacity or cold-cache evidence. No million-record run was added in this pass.

The indexed runner recorded the source and binary hash at its start. The later paired-read benchmark added only test-harness code; production engine code did not change afterward. Final checksums of the saved binaries are in `SHA256.json`.

- Indexed benchmark summary (local historical artifact: `database-three-backends-2026-10-13/indexed-matrix/summary.json`)
- Indexed samples and machine metadata (local historical artifact: `database-three-backends-2026-10-13/indexed-matrix/machine.json`)
- Direct driver samples (local historical artifact: `database-three-backends-2026-10-13/direct-drivers.json`)
- Paired point-read samples (local historical artifact: `database-three-backends-2026-10-13/paired-points.json`)
- SQLite full-flush diagnostic (local historical artifact: `database-three-backends-2026-10-13/sqlite-fullfsync.json`)
- Native integer-overflow witness (local historical artifact: `database-three-backends-2026-10-13/integer-overflow.log`)
- Full tests (local historical artifact: `database-three-backends-2026-10-13/tests.log`), race checks (local historical artifact: `database-three-backends-2026-10-13/race.log`)
- Local compiled sidecar (local historical artifact: `database-three-backends-2026-10-13/database-with-aptevadb`)

Reproduce the indexed comparison from the Database module:

```sh
GOTOOLCHAIN=local python3 benchmarks/run.py --rows 10000 100000 --repeats 3 --output /absolute/path/to/new-results
python3 benchmarks/summarize.py /absolute/path/to/new-results
```

Reproduce paired point reads:

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
 PAIRED_POINT_COMPARISON_OUTPUT=/absolute/path/to/new-paired.json \
 go test ./engine -run '^TestPairedPointDriverComparison$' -v -count=1
```
