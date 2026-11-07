# Database 0.14.0

Consolidates advanced reads into `find` and `aggregate`. Applications can join
calculated intermediate relations and aggregate their values in one read snapshot,
without a separate query operation or temporary business records.

## Canonical operations

- `aggregate` accepts ordinary collection metrics, relational aggregation, or
  `pipeline: {version: 1, nodes: [...], from: "relation"}` followed by final
  grouping and metrics. Intermediate results are never silently truncated.
- `find` accepts ordinary collection reads, joined and related reads, or the same
  named pipeline with filtering, projection, sorting, counts and signed cursors.
- The nine core data operations are `get`, `find`, `insert`, `update`, `upsert`,
  `delete`, `count`, `aggregate` and `batch`. Database and schema administration,
  indexing, realtime and maintenance operations remain available.
- Existing `query`, `joined_aggregate`, `joined_page` and `related_read` callers
  keep their request and response contracts. These discoverable compatibility
  aliases carry deprecation metadata pointing to the canonical operations.

See [READS.md](../READS.md) for the consolidated contract and migration examples,
and [QUERY.md](../QUERY.md) for named stages and numeric expression semantics.

## Execution and compatibility

All dependent stages and final reads share one snapshot and bounded execution
budget. Typed dependency validation, checked integer cents, explicit rounding,
join cardinality checks and existing null semantics remain in effect. Final
`aggregate` group limits report `truncated`; intermediate overflow fails the read.
`find` counts matches before paging and binds signed cursors to the plan and
consulted schemas. Existing joined-page cursors remain valid with canonical find.

Callers retain responsibility for deriving authorisation policy and supplying
scope filters before joins, distinct counts and aggregation. Business rules stay
caller-owned expressions. No storage migration or data-format change is required.

## Verification

- Full module tests, race tests, vet and standalone build passed during
  implementation. Release CI runs Go race tests and vet on Linux and macOS,
  module verification, realtime client tests and panel builds.
- SQLite, Pebble and AptevaDB pass the canonical dependent-aggregation fixture:
  September produces 4000 cents and October 2000 cents, totalling 6000 cents.
  The fixture excludes foreign-centre records and deduplicates callers.
- Coverage includes pipeline find pagination, counts, duplicate rows, legacy
  cursor compatibility, schema invalidation, empty inputs, numeric boundaries,
  resource limits, branching, joins, rollups and concurrent commits between scans.
- Existing MCP/HTTP operations, batch snapshot reads, read-grant isolation and
  ordinary native collection aggregation remain covered across the adapters.
