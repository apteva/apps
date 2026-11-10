# Database 0.16.0

Create a maintained computed collection on AptevaDB by passing
`computedCollection` to `db_collection_create`. Existing read tools query the
result as a typed, read-only collection. The engine backfills existing facts
and maintains the result and its indexes in the source write transaction.

A definition can filter and project source fields, or group by up to eight
non-null scalar fields. Rollups support counts, non-null counts and sums of
non-null integer fields. SQLite and Pebble return `unsupported_capability`
when asked to create a computed collection. Existing ordinary collections
work with their current schema and record operations.

See [computed collections](../README.md#computed-collections) for the request
format, lifecycle and limits.
