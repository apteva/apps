# Database 0.11.1

Released 5 November 2026. Source tag: `database/v0.11.1`.

One filter can now hold 24 `exists`/`not_exists` clauses, up from 8.

## Changes

- **Clause limit.** The limit across a filter's whole tree goes from 8 to 24. The 25th is refused with the limit error. `anyOf` alternatives still do not count as clauses.
- **Unchanged limits.** Depth 4, 16 alternatives and 8 pairs stay as they were, as do the nested work budget and the `query_too_expensive` failure, including under `not_exists`. Metric filters still refuse `exists`.

Filters that were already accepted return the same results.

## Performance

More clauses read more related records, so bound large filters with an indexed scope. A `count` with 16 clauses on real data, over 12,000 records with 125,865, 59,763 and 35,321 related records, on a heavily loaded machine:

| Related values allowed | With an indexed scope | No scope |
|---|---:|---:|
| One | 40–131 ms | 1.3–1.5 s |
| Largest | 59–79 ms | 1.7–2.4 s |
| Six | 99–200 ms | 5.7–8.8 s |

The slowest request seen was a `find` for a search matching nothing, with six values and no scope: 16.4 s, then 7.8 s. None of these reached the work budget; the heaviest used about a fifth of it. Key sets came close to their 16 MiB cap, so wider filters may fall back to more probes and take longer. A 24-clause filter on real data has not been timed.

No storage change: AptevaDB v0.2.0-alpha.18, format 10.
