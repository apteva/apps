# Database 0.12.0

Adds relational reads that run on one snapshot on SQLite, Pebble and AptevaDB. Database applies the filters it receives; access policy stays with the caller.

## New operations

- `related_read` returns parents with ordered, bounded related rows. A shared limit is filled source by source, and `match` tests only the rows kept, so a row past the limit can never match.
- `joined_page` joins through indexes, deduplicates by physical record or field, pages rows or groups, and computes the total at the same grain.
- `joined_aggregate` runs the existing metrics over joined, deduplicated rows. It adds `pick` for the first or latest qualifying row per key, `branches` to combine inputs, `rollup` for two-level grouping, and `derived` for division that is null on a null side or a zero denominator.

Joins are `inner`, `left`, `semi`, `anti` or `aggregate`, with explicit `text` and `numeric` comparison rules. All three operations can run in a read `batch`.

## Limits

- Every join alternative needs an index on a related field, or the read fails with `query_too_expensive`.
- Each read has a work budget and a 128 MiB memory budget. Exceeding either fails the read; it never returns partial rows.
- `joined_page` reads the whole joined input on every page so its total stays exact. Bound it with `where`.
- Filters cannot compare one field to another.

## Verification

- Direct acceptance tests on all three adapters, and over the sidecar's HTTP operations.
- A local Flexylead campaign conversion plan matched the saved Tables SQL for opportunities, contracts and rate, in five scopes on frozen local rows and on a synthetic fixture that exercises each rule. The frozen rows only exercise the explicit-origin path, so the other rules rest on the fixture.
- Other saved reporting queries have not been compared.
