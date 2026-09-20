# GraphQL 0.7.0

GraphQL 0.7.0 hardens the standalone GraphQL runtime for production use while preserving the existing Tables fast paths.

- Atomic API releases pin schema SDL, sources, resolver bindings, security, execution limits, and published resolver modules in one immutable snapshot. Releases can be inspected and rolled back in one operation.
- Cardinality-aware query costing accounts for list sizes and nested fan-out. Releases enforce depth, rows, resolver calls, response bytes, execution time, and bounded source parallelism with structured GraphQL error codes.
- Authenticated subscriptions now use the `graphql-transport-ws` protocol on `/public/realtime/{api_slug}`. Connections authenticate during `connection_init`, support multiple operation IDs, expire with the Auth session, reauthorize every event, and disconnect slow consumers.
- Resolver modules add strict or propagating null behavior, exact Decimal strings with configurable precision/scale/rounding, Date/DateTime/Duration operations with IANA timezones, completeness metadata, and batch evaluation.
- Asynchronous production telemetry now records the operation hash, API release, response size, row and resolver counts, source timing totals, structured error codes, authorization scope, and request ID without storing raw queries, variables, tokens, or claims.
- The dashboard exposes atomic releases, rollback, execution budgets, module runtime metadata, authenticated realtime guidance, and expanded operational state.

Compatibility: `graphql_schema_publish` and `graphql_deploy` now publish a full release rather than only changing schema status. Existing installations without a release continue to execute their legacy published schema until their first atomic release.
