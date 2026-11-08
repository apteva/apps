# Database 0.15.2

Fixes bounded pages whose unordered native input scan retained all matching
rows before Database's page heap saw them. No API, storage, permission-policy
or GraphQL change is required.

- Unordered adapter scans explicitly request the chosen index's complete
  physical order, or `$natural` when no indexed lookup applies. This avoids
  native global sorting and candidate-key materialisation, including multi-range
  IN filters. The complete predicate and internal join hints remain intact.
  Ordinary native find ordering semantics are unchanged.
- The existing bounded page heap still owns final result ordering, offsets and
  pagination. Its scan decodes only output, filter, ordering and tie-break fields.
- Native retained buffers, the current borrowed document, decoded scratch, a
  possible projected copy and the retained page share the configured work budget.
  A budget failure returns no partial page. The scan does not retry, change the
  snapshot or raise the query limit.
- Pending-write masking, cancellation, numeric ordering and primary-key ties are
  preserved. Qualified relational reads use the same streaming input path.

The regression fixture seeds 100,000 October records with separate native keys
and integer IDs, 40 centres and unused 1-KiB payloads, plus higher IDs outside
scope/date bounds. The 32-centre query returns exactly 80,000 matches and IDs
100000, 99991, 99990, 99989, 99988. Its actual execution retains no native sort
heap; the adapter retains under 2 KiB of page rows and accounts for under 5 KiB
combined scan/page work. SQLite supplies the cursor, offset and scope oracle.
Tests also cover count omission, duplicate IN values, boundaries, qualified
reads, shared-snapshot batches, staged writes, cancellation and combined-budget
failure. The full contract suite covers SQLite, Pebble and AptevaDB.

Physical streaming can traverse more index entries when multiple ranges cannot
supply the requested final order directly; memory remains bounded by one scan
record plus the page, rather than the number of matches. No temporary business
records or new indexes are introduced. SDK remains v0.115.0 and AptevaDB remains
v0.2.0-alpha.18.
