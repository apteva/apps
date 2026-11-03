# Database 0.7.0

Released 3 November 2026. Source tag: `database/v0.7.0`.

Filters gain a multi-field text search, `like_contains_ascii`, evaluated by the storage engine on every adapter.

## Changes

- **Multi-field text search.** A search box over several fields used to need either one `starts_with` per field (no infix, no cross-field match) or fetching the scoped records into the caller to filter them. One filter node now does it:

  ```json
  {"fields": ["resource_key", "opportunity_key", "commercial_id"], "op": "like_contains_ascii", "value": "23 op"}
  ```

  | Rule | Behaviour |
  |---|---|
  | Fields | 1–16 existing `text` fields; unknown or non-text fields are `invalid_argument` |
  | Nulls | Read as `""` |
  | Joining | Field values in the given order, separated by exactly one space |
  | Matching | `"%" + value + "%"` with SQLite's default `LIKE`: ASCII letters case-insensitive, other letters exact |
  | Wildcards | `%` and `_` stay wildcards (`_` is one UTF-8 character); backslash has no escape meaning |
  | Whitespace | Kept, never trimmed |
  | Empty value | Matches every record that the rest of the filter and the caller's scope allow; dropped from the plan |
  | Value | A string of at most 1024 bytes; other types are `invalid_argument` |

  The node combines with `and`/`or` and applies in `find`, `count`, `aggregate`, metric `where` and read batches, before pagination and aggregation. Each adapter evaluates it in the engine: AptevaDB compiles it to its `$like` operator (new in alpha.15), SQLite to `(coalesce(a,'')||' '||coalesce(b,'')||…) LIKE ?`, and Pebble with the same matcher. No adapter can use an index for an infix search, so the other conjuncts still choose the index range and the search only reads the records in it. `TestLikeContainsASCII` checks 21 values (wildcards, backslash, `Café`/`ÉTÉ`, padded spaces, matches across field boundaries) with and without a centre + date scope, on all three adapters, against a reference `LIKE`, through count, paginated find, grouped and bucketed aggregates, a metric `where` and a read batch.

- **AptevaDB v0.2.0-alpha.15.** Adds `$like`. Format 10 is unchanged; no migration. The flat document decoder is audited for alpha.15 (`doc.go` is unchanged).

## Performance

`BenchmarkLikeContainsASCII`, AptevaDB, 200,000 records, index `(centre, created_at desc)`, search value `"23 op"` over three fields, Apple M-series:

| Request | Centre + 90 days (≈1,640 in range) | Centre + 90 days + search | Search over all records |
|---|---:|---:|---:|
| `count` | 1.7 ms | 2.4 ms | ≈100 ms |
| `find`, 50 newest | 0.2 ms | 2.5 ms | ≈100 ms |
| `aggregate` by centre | 0.7 ms | 0.7 ms | 12–16 ms |

Inside the scope the search costs under a millisecond for count and aggregate. A paged `find` without a search stops after 50 records; with a rare term it reads the whole range to fill the page. An unscoped search reads every record, about 0.5 µs each, the same as SQLite's full-scan `LIKE` (78 ms over the same 200,000 rows), so keep the centre/date scope beside it. Aggregates shard that scan across cores; `count` and `find` read in one stream.

Requests valid on 0.6.5 behave as before.
