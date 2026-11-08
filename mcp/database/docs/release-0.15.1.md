# Database 0.15.1

Fixes selective indexed lookups with additional scope predicates. No API,
storage-format or GraphQL change is required.

- Streaming related joins preserve their selected index and the complete filter,
  including permission predicates. Projection, cancellation, query memory limits,
  pending writes and the read snapshot retain their existing semantics.
- Fully bound, non-null primary and unique keys take precedence over broad
  equality-prefix indexes. Partial or nullable unique keys keep ordinary planning;
  explicit internal hints retain precedence.
- AptevaDB compares competing non-unique equality indexes with capped index-only
  probes. Each query spends at most 1,024 index entries selecting an index; if
  inconclusive it keeps ordinary planning. Execution still returns every matching
  physical record, including duplicate alias values.
- Counts with residual predicates use the selected streaming lookup. This avoids
  the native count fallback discarding a non-covering hint and scanning a broad
  scope index. Count diagnostics report actual document fetches and separately
  expose optional `indexSelection` probe/entry counts.

Regression coverage seeds 10,000 calls, then doubles the centre to 20,000. Actual
related-join work stays constant, including exact/missing identifiers, wrong
centres, non-unique aliases, cross-alternative physical deduplication, and matching
find/aggregate parent counts. Focused tests also cover snapshot isolation and
staged writes. The full race suite exercises SQLite, Pebble and AptevaDB.

SDK remains v0.115.0; AptevaDB remains v0.2.0-alpha.18. Existing operations,
read-only batches, context caps and permission-policy ownership are unchanged.
