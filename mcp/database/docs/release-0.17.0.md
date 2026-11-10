# Database 0.17.0

Computed collections on AptevaDB can now aggregate several ordinary
collections into one read-only view. Each input can map its group fields
and declare filtered counts, integer sums, and exact distinct counts.
Changes to the inputs maintain the view in the source transaction.

Define online rollups with `sources` in `db_collection_create` and watch
their progress in `db_collection_describe`. Backfill runs in bounded chunks
and resumes after reopen. Read queries see the view only after all inputs
are current. Use `merge_distinct` in a direct `db_aggregate` to union the
identities from selected buckets on the query's snapshot.

Single-source definitions and ordinary collections retain their existing
behavior. The AptevaDB dependency is v0.2.0-alpha.20. See the
[computed collection examples](../README.md#computed-collections).
