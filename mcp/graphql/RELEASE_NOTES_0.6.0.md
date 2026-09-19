# GraphQL 0.6.0

- Adds versioned, reusable Resolver Modules for server-side computed GraphQL
  fields while keeping the public schema and query language standard GraphQL.
- Provides a validated typed expression model for arithmetic, decimal-safe
  intermediate calculations, comparisons, conditions, coalescing, strings,
  collection length, rounding, and composition through pinned module calls.
- Adds draft validation/testing, immutable publishing, version and usage
  inspection, MCP tools, admin HTTP endpoints, and a complete dashboard tab.
- Supports module inputs mapped from parent fields, GraphQL arguments,
  constants, and trusted identity values without JavaScript or `eval`.
- Preserves the optimized Tables route: computed-field dependencies are added
  to internal projection pushdown, resolver reads remain batched/parallel, and
  the guarded fast projection now completes module-backed fields.
- Includes the 0.5.2 read-path work: selection-aware exact totals,
  count/aggregate fusion, server/SDK parallel app batches with Tables-native
  fallback, and source-upsert correctness.
