# Database 0.6.2

Released 2 November 2026. Source tag: `database/v0.6.2`.

Dashboards that pass the authorised scope as a list now use the `(scope, day)` index. AptevaDB collections that already hold data can be indexed.

## Changes

- **A one-value `in` list is equality.** Callers such as GraphQL pass a single authorised scope as `{"field":"scope_id","op":"in","value":["internal"]}`. Every planner, in this app and in AptevaDB, binds an index prefix only on equality, so that filter skipped the `(scope_id, day)` index and scanned the collection. Validation now rewrites a one-value list to `eq`. This covers every query, count, aggregate, metric `where`, update and delete, on every adapter. Results are identical: `in` and `eq` already compared values the same way. Empty lists and nulls are still rejected, and lists of several values keep their meaning. `requireIndex` now accepts these filters.
- **Pin AptevaDB v0.2.0-alpha.13: lists of several values use the index too.** Alpha.13 reads one compound-index range per combination of listed values on the leading fields, up to 64, so a user with several scopes no longer scans either. `TestNativeAggregateUsesCompoundIndex` now also covers a two-scope list in the adapter's nested `$ne null` form, and fails on alpha.12. Alpha.13 joins the projected decoder's audited versions (`doc.go` unchanged).
- **AptevaDB builds indexes on populated collections.** `index_create` first loaded up to 10,001 full records to count them and check uniqueness. Wide records hit the 16 MiB retained-results limit first (`429 resource_limit: retained query results exceed 16 MiB`), so no index could be added to an existing collection. AptevaDB now counts the records natively and builds the index itself, checking uniqueness before it publishes the index. A failed build rolls the schema back. Builds run up to 1,000,000 existing records; through the app, indexing 328,000 existing records of about 600 bytes took 0.46 s (one field) and 0.86 s (two fields); above that, the error states the cap and the collection's size. `index_create` gets a 30 s deadline on every adapter. SQLite and Pebble keep their 10,000-record limit.

No API or storage change. AptevaDB format remains 10.

## Performance

`BenchmarkScopedDayAggregates` uses 328,000 records of about 600 bytes over 4 scopes and 26 days, with a `(scope_id, day)` index, filtered to one day. It runs one grouped aggregate, and a read batch of 14 conditional aggregates (ms, 3 interleaved runs):

| Scope filter | 0.6.1 aggregate | 0.6.2 aggregate | 0.6.1 batch of 14 | 0.6.2 batch of 14 |
|---|---:|---:|---:|---:|
| `eq` | 1.1–1.2 | 1.3–1.5 | 15.6–17.8 | 16.7–18.0 |
| one-value `in` | 23.2–26.5 | 1.3–1.4 | 343–353 | 17.6–32.9 |
| two-value `in` | 25.1–27.1 | 2.5–3.7 | 345–383 | 33–51 |

The two-value list matches twice the records. The 0.6.1 two-value figures were measured on alpha.12, the same engine 0.6.1 ships.

Read batches still run their operations one after another on one snapshot. With these aggregates at 1–4 ms, running them in parallel is not worth its complexity yet.
