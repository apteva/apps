# Database 0.10.1

Released 4 November 2026. Source tag: `database/v0.10.1`.

Key sets for `exists` filters now read only the related records an index selects, and AptevaDB scans no longer allocate memory for fields they skip.

## Changes

- **Bounded key set scans.** Building a key set used to read the whole related collection and then apply the clause's `where`. It now seeks the index prefix that the ordinary part of `where` can use, and the complete `where` still decides every key. Terms outside that prefix stay out of the scan so that every row read counts against the build's row allowance. With no usable index, or with an `or` holding a relationship, the scan stays full and the allowance stops it, as before. Applies to `exists`, `not_exists` and `anyOf`, on AptevaDB and Pebble.
- **`relatedDocumentsExamined` diagnostic.** The `exists` section of count diagnostics now reports the documents key set scans actually read, next to `relatedRowsRead`.
- **Projected scan decoding.** The fast decoder heap-allocated a temporary for every field of every row, whether the query read that field or not, because JSON decoding took its address. JSON now decodes through its own variable. Reading one of 46 fields goes from 47 allocations per row to at most 2, checked by `TestNativeFlatDecoderAllocatesOnlyWantedFields`.

Results are unchanged: the decoder still validates every field, wanted JSON fields decode as before, and probe-only, forced-set and default execution return the same rows, counts and pages.

## Performance

A count filtered by either of two relationships, each with a selective `where` on an indexed field, over 10,700 records with 100 and 125,865 related records, 137 matching, repeated twice after a first run. This includes the decoder change:

| | 0.10.0 | 0.10.1 |
|---|---:|---:|
| Warm count | 1,069–1,131 ms | 35–37 ms |
| Related rows read | 252,946 | 1,552 |
| Probes | 4,224 | 256 |

The decoder change alone:

A nested count over 12,000 records, 125,865 wide related records and 35,321 further related records, on a copy of real data, repeated three times: 1,080 ms on 0.10.0, 440 ms on 0.10.1.

Benchmarks, AptevaDB, Apple M-series, 3 runs each. The gain grows with record width, so these narrower fixtures gain less:

| Request | 0.10.0 | 0.10.1 |
|---|---:|---:|
| `BenchmarkNestedExists`, `exists` of `exists`, `count`, all records | 311 ms | 225 ms |
| `BenchmarkNestedExists`, `exists` of `exists`, `count`, one scope | 90 ms | 53 ms |
| `BenchmarkExistsAndNotExists`, `count`, all records | 474 ms | 355 ms |
| `BenchmarkExistsAndNotExists`, `find` 50 rows, all records | 653 ms | 455 ms |

The first request after a long idle can still take several seconds while data is read back into memory. That is not changed here.

No storage change. AptevaDB stays on v0.2.0-alpha.17, format 10. Requests valid on 0.10.0 behave as before.
