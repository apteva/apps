# Realtime implementation and performance

14 October 2026. Pre-release measurements on `fix/database-native-performance`; AptevaDB `v0.2.0-alpha.5`, app-sdk `v0.82.0`. Installed application data and settings were not changed. Database 0.5.0 subsequently updated its release pin to app-sdk v0.102.0; the measured engine paths are unchanged and the real SDK stream/compatibility suite was rerun for that pin.

Native capture, atomic initial snapshot, signed resumable collection invalidations, bounded MCP polling and SSE are implemented. The dashboard can enable capture and refresh records/submitted aggregates. SQLite/Pebble persistent change journals remain a later phase. See [usage and limits](../REALTIME.md).

## Capture and watcher overhead

Three fresh 100,000-row fixtures per cell, with separate app/direct fixtures. The schema has a primary index, numeric amount, 128-byte note and normal record metadata. Capture is enabled before seeding. Each cell reports the median of the three per-fixture p50s. App and direct paths use identical data encoding, synchronous durability and response JSON encoding. Reads below are with idle subscribers; writes actively deliver every timed commit to every subscriber. No subscriber failure/missed commit is accepted as a successful timing.

### Ordinary fsync: SyncOS

| Capture | Watchers | Single insert, µs (app/direct) | 1,000-row insert, ms (app/direct) | Batch range across app fixture p50s, ms |
|---|---:|---:|---:|---:|
| Off | 0 | 72.21 / 66.04 | 4.992 / 3.851 | 4.353–5.042 |
| On | 0 | 67.25 / 59.38 | 4.747 / 3.509 | 4.003–4.833 |
| On | 1 | 67.12 / 65.79 | 4.209 / 3.426 | 4.059–4.327 |
| On | 10 | 77.88 / 61.62 | 4.375 / 3.677 | 4.328–4.383 |
| On | 32 | 92.83 / 94.17 | 4.321 / 3.447 | 3.999–5.167 |

With 32 watchers, the adapter batch median differed from capture-off by -13.4%. Single inserts changed from 72.21 to 92.83 µs.

### Full drive-cache flush: SyncFull

| Capture | Watchers | Single insert, µs (app/direct) | 1,000-row insert, ms (app/direct) | Batch range across app fixture p50s, ms |
|---|---:|---:|---:|---:|
| Off | 0 | 3969.21 / 4070.29 | 8.825 / 8.004 | 8.773–8.974 |
| On | 0 | 4122.79 / 4001.50 | 9.141 / 8.084 | 8.603–9.682 |
| On | 1 | 4732.50 / 4038.54 | 8.945 / 8.308 | 8.630–9.392 |
| On | 10 | 3955.17 / 3965.17 | 8.458 / 8.192 | 7.858–9.774 |
| On | 32 | 3892.62 / 3958.62 | 8.810 / 8.875 | 8.620–10.680 |

With 32 watchers, the adapter batch median differed from capture-off by -0.2%. Single inserts changed from 3969.21 to 3892.62 µs.

The watch matrix was repeated after builds and correctness checks finished; the tables use that clean rerun. Preliminary samples are preserved in watch-*-preliminary.json, including full-flush fixture p50s up to 19.9 ms. Clean full-flush fixture ranges were narrower, though one 32-watcher fixture still reached 10.68 ms. Batch results do not show a consistent capture-related slowdown, and faster capture-on medians must not be interpreted as a speedup caused by capture. Single ordinary-fsync writes do have noticeable fan-out cost at 32 watchers. These are sampled workload results, not a guarantee that every timing is flat.

## HTTP stream delivery

Actual local HTTP writer route → complete streamed SSE frame, including commit durability, application handling and loopback HTTP. 100 single-document commits; every subscriber observes every commit. This excludes the production gateway, internet/TLS and browser rendering.

| Flush mode | Subscribers | p50, ms | p95, ms |
|---|---:|---:|---:|
| os | 1 | 0.162 | 0.223 |
| os | 8 | 0.205 | 0.290 |
| full | 1 | 3.996 | 6.135 |
| full | 8 | 4.062 | 6.433 |

The lower-level watcher matrix also records commit-return-to-notification samples in its raw files. These are clipped at zero when a watcher receives a durable commit before the write API has finished returning/encoding its response; zero does not mean instantaneous networking. Dashboard refresh adds a 100 ms debounce (500 ms maximum postponement under sustained notifications), query time and rendering. Browser paint latency was not measured.

## Adapter versus direct database drivers

Fresh 100,000-row fixtures, three repeats per backend/mode; 1,000 point reads, 15 counts and SUMs, 100 single inserts and 20 batches of 1,000. Capture off for these reference comparisons. Timings include matching output JSON. App/direct order reverses in the middle repeat and backend order rotates. Stored output keys and resulting cardinality are verified.

### Ordinary fsync

| Operation | aptevadb | sqlite |
|---|---:|---:|
| Point read, µs | 4.958 / 4.250 | 10.916 / 10.083 |
| Count, ms | 3.772 / 3.340 | 8.614 / 6.739 |
| SUM, ms | 3.634 / 3.321 | 12.883 / 12.831 |
| Single insert, ms | 0.051 / 0.051 | 0.090 / 0.091 |
| 1,000-row insert, ms | 3.907 / 3.269 | 4.140 / 3.525 |

### Full flush

| Operation | sqlite | pebble | aptevadb |
|---|---:|---:|---:|
| Point read, µs | 11.375 / 10.125 | 14.875 / 14.208 | 5.292 / 4.833 |
| Count, ms | 8.640 / 6.956 | 17.979 / 17.703 | 3.815 / 3.270 |
| SUM, ms | 13.917 / 15.236 | 38.738 / 44.315 | 3.917 / 3.479 |
| Single insert, ms | 4.051 / 4.036 | 4.017 / 4.054 | 4.059 / 3.673 |
| 1,000-row insert, ms | 8.953 / 7.819 | 26.043 / 25.254 | 8.948 / 7.310 |

Both SQLite writer connections were reserved and checked at synchronous=FULL, with fullfsync OFF in the ordinary profile and ON in the full profile. AptevaDB uses Sync=true with the matched OS/full flush option. Pebble stays synchronous full flush, so it is only included in the full profile. Matching the requested flush primitive does not establish identical crash semantics. SQLite is the same modernc Go driver used by the app, not a separate C SQLite executable. Pebble has no aggregate API; its direct reference uses raw snapshot/index iterators with the shared checked Go reducer and projected JSON codec.

## Million-row aggregate checks

Two fresh processes/fixtures per configuration; five trials per successful shape/mode. App/direct order alternates and both operate on the same fixture. Live AptevaDB capture is enabled before loading the million records, with ten subscribers opened after loading and left idle during read measurements. This tests the cost of retained capture state/idle watchers, not aggregate latency during concurrent writes.

Cells are **adapter/direct milliseconds**.

| Operation | AptevaDB capture off | AptevaDB capture on + 10 watchers | SQLite | Pebble |
|---|---:|---:|---:|---:|
| Numeric, four groups | 42.85 / 48.01 | 42.95 / 47.63 | 728.90 / 690.60 | 686.27 / 680.98 |
| Numeric, 1,000 groups | 46.24 / 52.09 | 46.81 / 53.39 | 4486.38 / 4260.05 | 712.79 / 706.00 |
| Integer, four groups | 45.47 / 49.44 | 45.87 / 49.77 | 626.94 / 620.80 | 801.41 / 750.70 |
| Nullable counts, four groups | 47.03 / 49.45 | 48.29 / 47.31 | 598.88 / 566.56 | 638.95 / 620.06 |
| Indexed tenant, 1,000 rows | 1.53 / 1.13 | 1.56 / 1.27 | 4.57 / 4.25 | 12.37 / 10.21 |

The million-unique-group, sorted COUNT top-1 case still intentionally fails the app’s 16 MiB retained-work budget on AptevaDB/Pebble; SQLite succeeds because SQLite VM memory is outside that Go budget. A limit of one does not bound the number of input groups. These expected errors were verified, not reported as fast successful aggregates.

Peak process RSS across the two million-row fixture runs: aptevadb-off 496.8–499.2 MiB; aptevadb-live 489.5–516.1 MiB; sqlite 40.8–43.9 MiB; pebble 44.4–48.0 MiB. The native data/cache path remains substantially heavier than SQLite/Pebble. Native capture accounting and logical retention are not RSS or physical disk caps. Raw watcher results include native reader/cache/plan accounting and database directory sizes.

## Verification and artifacts

- Full Go suite, Go race suite, `go vet`, `go mod verify`, standalone sidecar build and Bun client tests/panel build passed.
- The real SDK sidecar stream test checks bearer authentication, snapshot/ready/invalidate delivery and cursor cleanup on disconnect.
- Realtime checks cover snapshot/write races, rollback, replay, scope/spec forgery, catalog isolation, drop/recreation, restart, expiry, disable/reenable and quotas; an idle subscriber does not block CRUD.
- A genuine alpha.4 format-9 fixture was copied to a backup before alpha.5 opened it. Format-10 upgrade preserved an exact MaxInt64 document and emitted a native watch event. Alpha.4 then refused the upgraded data without changing file hashes; the original backup remained readable.

Raw logs, samples, probes, runner scripts and source/binary hashes: `results/database-realtime-2026-10-14`. The clean watcher matrix uses the final engine source. The driver reference and million-row binaries were compiled before the final initial-deadline validation/registry retention join; their timed CRUD/aggregate paths were unchanged. The final sidecar binary is `database-realtime`. The earlier HTTP timing file measured event-header arrival; the report uses `http-complete-frame.json`, rerun with complete frame delivery.

Local macOS measurements are workload-specific. Read medians below native medians can occur due to noise and different normalization paths; they do not prove an adapter is intrinsically faster than its underlying engine. No production deployment or network/browser latency claim is made.
