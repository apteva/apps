# Database 0.9.1

Released 4 November 2026. Source tag: `database/v0.9.1`.

On AptevaDB, filters that need documents, such as a text search or `exists` within a multi-value `in` scope, now use the index for much larger scopes.

## Changes

- **AptevaDB `v0.2.0-alpha.17`.** A filter that needs documents reads them by key while the index names at most a quarter of the collection, counted exactly. This replaces the eighth stated for 0.9.0, which was applied to an estimate that can be several times off in either direction: on 125,865 real records it read 45,489, so narrowing stopped near 5,700 entries instead of about 15,700.
- Only `in` scopes of several values change. An equality or a one-value `in` already read through the index with no cutoff.
- Results are unchanged, and no request changes shape.

## Performance

A multi-value `in` scope with a text search, counted by AptevaDB on 125,865 real records, best of 5, warm. These were measured on AptevaDB directly, not through this app:

| Records in scope | 0.9.0 (alpha.16) | 0.9.1 (alpha.17) |
|---:|---:|---:|
| 4,567 | 10.7 ms | 12.1 ms |
| 6,116 | 65.4 ms | 14.6 ms |
| 13,693 | 78.8 ms | 32.2 ms |
| 25,737 | 99.7 ms | 59.3 ms |
| 31,803 | 109.7 ms | 111.2 ms |
| 62,537 | 158.4 ms | 160.3 ms |

The first row reads by key on both, so that difference is noise. Scopes past a quarter of the collection still scan. `exists` scopes take the same route but were not timed separately, and cold reads were not measured.

No storage change. AptevaDB stays on format 10. Requests valid on 0.9.0 behave as before.
