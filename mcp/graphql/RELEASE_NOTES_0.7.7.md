# GraphQL v0.7.7

GraphQL v0.7.7 adds a fast, generic multi-table aggregation resolver while
keeping the public API fully standard GraphQL.

## `aggregate_pipeline`

- Adds a separate Tables resolver operation for joins, CTEs, window functions,
  latest-row deduplication, grouping, and conditional counts/sums.
- Executes each field as one native `tables_query`; sibling fields continue to
  use the existing bounded server/Tables batch path.
- Keeps SQL, source names, identifiers, and ordering immutable in versioned
  resolver configuration. Clients only provide schema-declared GraphQL values.
- Supports positional parameters from coerced `$args`, `$parent`, verified
  `$identity.subject`, `$identity.tenant`, `$identity.claim.*`, and fixed
  literals, with string, number, boolean, RFC3339 datetime, and JSON types.
- Supports `rows`, `single`, and `envelope` result modes with strict
  cardinality, row, truncation, release, deadline, and response-size limits.
- Preserves parent columns needed by nested pipelines during Tables projection
  pushdown.

## Security

- Every `{table_name}` placeholder must belong to an explicitly declared,
  active Tables source in the same API release.
- Pre-scoped Tables sources and automatic GraphQL `row_filters` are rejected
  for pipelines because they cannot be safely injected into arbitrary SQL.
  Authorization restrictions belong in fixed resolver SQL with verified
  identity parameters.
- Tables v0.1.26 remains responsible for read-only SQL validation, project
  isolation, physical-table authorization, query-only execution, deadlines,
  row caps, and response-byte caps.

## Performance and validation

- Unit, race, vet, UI bundle, batching, parameter, truncation, and cardinality
  checks pass.
- A real Tables v0.1.26 test over 10,000 prospects, 20,000 calls, and 10,000
  sales returned identical metrics. The multi-table query measured 59.0 ms p50
  directly through Tables and 59.6 ms p50 through GraphQL (20 samples), about
  0.6 ms incremental median overhead in the isolated sidecar harness.
