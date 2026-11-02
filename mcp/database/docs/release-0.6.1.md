# Database 0.6.1

Released 2 November 2026. Source tag: `database/v0.6.1`.

AptevaDB aggregates now read through compound indexes. A dashboard filtered by scope and day used to scan the whole collection for every aggregate, even with a `(scope_id, day)` index.

## Changes

- **Pin AptevaDB v0.2.0-alpha.12.** Up to alpha.11, the native aggregation planner considered single-field indexes only. Every Database index on AptevaDB is created as an exact compound index, so a multi-field index never narrowed an aggregate, and neither did a descending one. `find` already used them. Alpha.12 applies them to aggregates with the bounds `find` uses: an equality prefix plus an optional range on the next field. This covers the adapter's `$ne null` + range filter shape.
- **`db_explain` and `requireIndex` are accurate for aggregates again.** Both check the adapter's own planner, which accepts compound indexes. On alpha.11 they reported a compound-index aggregate as indexed while the engine scanned. `TestNativeAggregateUsesCompoundIndex` now fails if the pinned AptevaDB stops narrowing aggregates through a compound index. It fails on alpha.11.
- Pin app-sdk v0.113.0, the latest tag (linear after v0.111.0: gateway keep-alive pooling and additive manifest fields).
- `doc.go` is byte-identical in alpha.12, so alpha.12 joins the projected decoder's audited versions.
- The skill explains that aggregates and dashboard batches use the same indexes as `find`.

No API, schema or storage change. AptevaDB format remains 10.

## Performance

`BenchmarkScopedDayAggregates` (new) loads 328,000 records of about 600 bytes over 4 scopes and 26 days, with a `(scope_id, day)` index. About 3,150 records match one scope and day. It runs one grouped aggregate, and a read batch of 14 conditional aggregates, the shape of the reported dashboard. The alpha.11 and alpha.12 test binaries were interleaved, 6 runs each:

| | 0.6.0 (alpha.11) | 0.6.1 (alpha.12) |
|---|---:|---:|
| One aggregate | ≈ 20 ms (19.4–28.9) | ≈ 1.2 ms (1.10–2.96) |
| Batch of 14 aggregates | ≈ 282 ms (274–441) | ≈ 15.6 ms (15.0–18.2) |

SQLite and Pebble are unchanged.
