# Database 0.9.0

Released 4 November 2026. Source tag: `database/v0.9.0`.

Scoped counts on AptevaDB can now read index entries alone, multi-value `in` filters use the index, and `count` can report what it actually did.

## Changes

- **Multi-value `in` as index ranges.** A filter such as `region in (a, b) and created_at in a date range` used to find no index on Pebble and AptevaDB, and read every record. `plan()` now expands `in` on the leading fields of an index into a union of ranges, each bounded by a range on the next field.

  | Rule | Behaviour |
  |---|---|
  | Values | Deduplicated by their typed encoding, so strings, nulls and boundaries keep their exact meaning |
  | Bounds | At most 64 value combinations and 1 MiB of range bounds; past that the filter still applies over a scan |
  | Records | Each counts once; distinct values give disjoint ranges |
  | Residual | The complete filter still applies wherever documents are read |
  | Snapshot | Every range reads the operation's one snapshot |
  | `requireIndex` | A union now satisfies it |

- **AptevaDB v0.2.0-alpha.16: index-only counts.** A count covered by one compound index reads entries without fetching documents: equality or `in` on leading fields, an optional following range, and non-null checks where those prove them. Filters needing documents, such as text search, uncovered fields or `exists`, read only the records in those ranges when they name at most an eighth of the collection, and otherwise scan.

- **Paged `find` weighs its routes on AptevaDB.** A union has to read every match to sort a page, whereas an ordered stream can stop early. When both exist, `find` counts the matches from the index and picks the cheaper. Pebble keeps the stream unless `requireIndex` needs the union.

- **`count` diagnostics.** `diagnostics: true` reports the plan and index actually used, whether it was index-only, documents fetched, index entries examined, examined and returned counts, peak memory, and queue, snapshot and execution times, plus probes and key sets for `exists` and `not_exists`. Only AptevaDB reports engine statistics; Pebble and SQLite report timings and the planned access, marked `"planSource": "explain"`. `explain` now says `"executed": false`.

`TestInPrefixRanges` checks these filters against a brute-force reference on all three adapters, through count, aggregate, read batch and multi-page find in three orders, including nulls, duplicate values, descending indexes and integer fields. In AptevaDB, `TestCountRangesMatchDocumentEvaluation` compares index counts with document evaluation over 6,000 randomised filters, including mixed types, large integers and buffered writes.

## Performance

AptevaDB, 125,000 records of about 2 KB, index `(scope, at)`, a date range holding a quarter of the records, Apple M-series:

| Request | 0.8.0 | 0.9.0 |
|---|---:|---:|
| `count`, one value + range | 1.2 ms | 0.04 ms |
| `count`, two values + range | 50 ms | 0.03 ms |
| `count`, 30 values + range | 165 ms | 0.11 ms |
| `count`, two values + range + text search | 50 ms | 4.7 ms |
| `aggregate` count, two values + range | 50 ms | 0.03 ms |
| `find` 50 rows by date, two values + range | 56 ms | 6.6 ms |
| `find` 50 rows by date, 30 values + range | 152 ms | 156 ms |
| `find` 50 rows in key order, two values + range | 1.7 ms | 1.7 ms |
| `find` 50 rows in key order, 30 values + range | 0.33 ms | 0.62 ms |
| `find` 50 rows in key order, rare value | 78 ms | 0.19 ms |

Weighing the routes adds about 0.3 ms to a paged `find` with both available. A broad `in` sorted by a field no index can supply still reads and sorts every match.

No storage change. AptevaDB format stays 10. Results of requests valid on 0.8.0 do not change. Responses gain `executed` and `note` on `explain`.
