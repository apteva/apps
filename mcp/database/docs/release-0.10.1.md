# Database 0.10.1

Released 4 November 2026. Source tag: `database/v0.10.1`.

AptevaDB scans that read a few fields of wide records no longer allocate memory for the fields they skip.

## Changes

- **Projected scan decoding.** The fast decoder heap-allocated a temporary for every field of every row, whether the query read that field or not, because JSON decoding took its address. JSON now decodes through its own variable. Reading one of 46 fields goes from 47 allocations per row to at most 2, checked by `TestNativeFlatDecoderAllocatesOnlyWantedFields`.

Results are unchanged: the decoder still validates every field, and wanted JSON fields decode as before.

## Performance

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
