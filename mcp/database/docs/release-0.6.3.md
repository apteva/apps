# Database 0.6.3

Released 2 November 2026. Source tag: `database/v0.6.3`.

On AptevaDB, a read batch runs up to four of its reads at the same time, still on one snapshot.

## Changes

- **Read batches run their reads concurrently on AptevaDB.** A batch of only reads (`get`, `find`, `count`, `aggregate`) used to run its operations one after another. On AptevaDB it now runs up to four at a time, at most one per CPU. They all read the same snapshot: a test runs a writer during batches, and every read in a batch still sees the same data.
  - **Results and errors.** Results keep operation order. The first failure in operation order fails the whole batch. No further read starts after a failure or once the results exceed the 4 MiB response limit.
  - **Memory and workers.** Every read keeps its own 16 MiB work budget, so one batch holds at most 64 MiB of query memory, which counts against AptevaDB's managed-memory budget. A broad aggregate still runs AptevaDB's own parallel workers (four by default).
  - **Filters.** Validation normalises each request's filters in place (for example the one-value `in` → `eq` rewrite). Each concurrent read therefore gets its own copy of its request, decoded as from the wire, so reads that share filter objects cannot race.
  - **Other adapters.** SQLite and Pebble still run a batch's reads one after another.
- **Guidance: merge reads that share a filter.** When reads share a collection, filter and `groupBy`, one aggregate with a `where` on each metric reads the records once instead of once per read. That is much faster than any concurrency (see below). The README and skill now say so. The skill also says never to derive a global distinct count by summing per-group distinct counts.

No API, response or storage change. AptevaDB stays on v0.2.0-alpha.13, format 10.

## Performance

`BenchmarkReadBatchConcurrency` uses AptevaDB with 200,000 records of about 700 bytes over 4 scopes and 31 days, with a `(scope_id, day)` index. It runs a read batch of 14 grouped aggregates over one scope, each counting one status. "Month" covers a quarter of the collection, past AptevaDB's index cap, so every read scans. "Day" reads one indexed day. Five runs each, ms, on a 10-core Mac:

| Batch of 14 | Month (scan) | Day (indexed) |
|---|---:|---:|
| 1 at a time (0.6.2) | 209–256 | 7.1–8.0 |
| 2 at a time | 138–175 | 6.1–6.5 |
| **4 at a time (0.6.3)** | **102–110** | **5.5–5.8** |
| The same 14 counts as filtered metrics of one aggregate | 17.3–18.2 | 0.87–0.92 |

Eight at a time measured no faster than four (115–163 ms against 106–150 ms in a later run).
