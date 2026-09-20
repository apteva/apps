# GraphQL 0.7.6

GraphQL 0.7.6 adds generic server-side relationship filters and native Tables
v0.1.25 pushdown without changing the public GraphQL query language.

- Adds validated resolver-owned `relation_filter` plans with nested boolean
  predicates, multiple parent values, `coalesce`, casts, multi-key matching,
  and correlated `exists` / `not_exists`.
- Compiles plans to Tables `filter_ast` v1 after capability negotiation, so
  filtering happens before pagination, totals, aggregation, and result JSON.
- Retains request-local Tables batching for native filtered reads.
- Preserves fixed source, identity-derived, client, and relationship filters by
  composing them with AND; clients cannot choose tables or correlated sources.
- Validates plans when resolvers are saved, when atomic releases are published,
  and again at runtime.
- Retains a bounded, fail-closed GraphQL fallback for older Tables runtimes.
- Adds unit, batching, pagination, scan-cap, and real Tables v0.1.25 sidecar
  regression coverage.

The Tables dependency is now `>=0.1.25` for new installations.
