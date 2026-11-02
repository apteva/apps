# Database 0.6.0

Prepared 2 November 2026. Source tag: `database/v0.6.0` (not yet created).

Dashboard queries can now be answered by Database itself, natively on AptevaDB alpha.11: distinct counts, conditional metrics, many-to-one lookups, timezone calendar buckets, and several reads that agree because they come from one snapshot. The operation list is unchanged. `aggregate` and `find` gain optional arguments, and `batch` also accepts reads. AptevaDB moves to alpha.11, which also fixes wrong counts that 0.5.3 could return.

## Changes

- **`count_distinct` metric.** Counts distinct non-null values of a scalar field, or of a bucket (for example active days per caller).
- **Per-metric `where`.** Any metric can be restricted to matching rows (`sales` = count where status is sale, `revenue` = sum of amount where status is sale), so conditional metrics share one pass instead of one query each.
- **`lookups` on `aggregate` and `find`.** `{as, collection, localField, select?}` attaches the one record of another collection in the same database whose single-field primary key equals `localField`. The join is many-to-one, so it cannot duplicate counted rows. A null or dangling reference gives null fields. In aggregates, `as.field` works in `where`, `groupBy`, metrics and metric filters, and a lookup can start from an earlier lookup's field. In `find`, the record is nested under `as`.
- **`buckets` on `aggregate`.** `{name, field, unit, timezone?, weekStart?}` derives `hour`, `day`, `week`, `month` or `year` keys from a datetime in an IANA timezone (default UTC). Calendar units return the local start date. `hour` returns the UTC instant the local hour starts, so it stays unambiguous across daylight-saving changes and half-hour offsets. Timezone data is compiled into the binary.
- **Read batches.** A `batch` of only `get`, `find`, `count` and `aggregate` runs in one read transaction, so every result comes from the same snapshot. It needs read access only (a `read` app grant is enough), accepts `timeoutMs`, and limits the combined response to 4 MiB. Reads and writes cannot be mixed; write batches are unchanged.
- **AptevaDB v0.2.0-alpha.11.** Its `doc.go` codec description is byte-identical to alpha.10, so alpha.11 joins the projected decoder's audited versions. Alpha.11 fixes a lone grouped `$count`/`$countNonNull` run with `AllowDiskUse`, which alpha.9 and alpha.10 returned mostly as 0. **Database 0.5.3 is affected:** a count-only aggregate whose group state exceeds the native work budget retries through that disk-enabled stream. Groups holding several rows then come back with wrong counts. One-row-per-group results were correct. With the query budget set to 1 MiB, the released 0.5.3 binary returned counts of 0 and 2 for 3,000 groups of 4 rows; 0.6.0 returns 4. At the default 16 MiB the same happens once the groups exceed the budget. Only AptevaDB databases are affected.

## Execution

- **AptevaDB native mapping.** The extensions run in the native parallel pipeline and its bounded spill stream:
  - stored-field filters become a leading `$match`;
  - each lookup becomes `$lookup` on the target's primary-key field, then `$unwind` with `preserveNullAndEmptyArrays`;
  - joined-field filters become a second `$match`;
  - buckets become `$dateTrunc` group keys. Hour buckets are reformatted to the app's microsecond datetime form, and weeks pass `startOfWeek` explicitly, because alpha.11 defaults to Sunday.
  - metrics become filtered accumulators and `$countDistinct`. Hidden count, min and max guards for a filtered integer sum carry the same filter.
  - group keys are aliased inside `_id`, since joined names contain dots.
- **Lookup memory bound.** Alpha.11 caps a lookup's hash table by row count only (default 1M rows), not bytes. With lookups, Database caps it at one target row per 512 bytes of work budget (32,768 at 16 MiB). A larger target falls back to the portable path. A follow-up for AptevaDB is to account join tables in `MaxBytes`.
- **SQLite.** `count_distinct` and metric filters compile to native `COUNT(DISTINCT …)` and `FILTER (WHERE …)`.
- **Portable path.** Used for SQLite requests with lookups or buckets, every extended Pebble request, and, on AptevaDB, buckets used outside `groupBy`, staged writes and over-cap lookups. It runs on the request's snapshot:
  - filter conjuncts on stored fields are pushed down to the scan;
  - conjuncts on joined or derived fields run after enrichment;
  - joined records are read by primary key and cached within a quarter of the work budget;
  - bucket keys are cached per stored UTC hour when that whole hour falls in one bucket.

## Performance

`BenchmarkDashboardAggregates` (`engine/analytics_benchmark_test.go`) uses 100,000 calls over 50 campaigns and 5,000 callers. Ten iterations ran on a loaded workstation (load average 8–10 on 10 cores), so these are indicative only:

| Query | SQLite | Pebble | AptevaDB alpha.11 (native) | AptevaDB, portable path |
|---|---:|---:|---:|---:|
| Plain grouped count + sum | 54 ms | 55 ms | 3.5 ms | — |
| Conditional metrics | 59 ms | 67 ms | 5.3 ms | 49 ms |
| Distinct callers per campaign | 65 ms | 60 ms | 6.1 ms | 47 ms |
| Lookup, grouped by campaign name | 71 ms | 62 ms | 5.8 ms | 45 ms |
| Paris day buckets | 77 ms | 64 ms | 5.3 ms | 51 ms |
| All of the above in one aggregate | 178 ms | 137 ms | 22 ms | 118 ms |

The last column is the same build before the native mapping, on alpha.10. On AptevaDB the extensions now cost 1.5–1.8× a plain native aggregate, and 6× when all of them run in one query. On SQLite and Pebble they cost 1.1–1.5×.

## Validation

- `TestAnalyticsNativeMatchesPortable` runs seven extended aggregates on one AptevaDB snapshot through the native parallel pipeline, the native stream and the portable accumulator, and requires identical results. The cases cover lookups, chained lookups, joined-field filters, all bucket units including Kolkata hours and Sunday weeks, filtered and distinct metrics, and empty filters.
- `TestAnalyticsLargeLookupFallsBack` joins a target over the row cap. It fails with `resource_limit` if the portable fallback is removed.
- `TestAnalyticsAdapters` runs every extension on all three adapters with exact expected values, covering:
  - null and dangling references, and chained lookups;
  - filters mixing stored and joined fields;
  - Paris dates across the 29 March 2026 summer-time change, Monday and Sunday weeks, and Kolkata half-hour offsets;
  - read batches, including mixed batches being rejected;
  - 12 kinds of invalid request.
- The release package check runs that 3,000-group count on the packaged binary. The same check fails on the 0.5.3 binary.
- `TestAppProjectDatabaseAccess` checks that a `read` app grant can run a read batch with `timeoutMs` but not a write batch.
- The full suite passes with `GOWORK=off` against the public alpha.11 module, and the race detector passes on the adapter, AptevaDB and analytics tests.
