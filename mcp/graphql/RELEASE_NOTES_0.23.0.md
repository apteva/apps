# GraphQL 0.23.0

Adapts Database resolvers to Database 0.14.0's consolidated `find` and `aggregate`
API. A resolver can send a named scan/compute/join/aggregate dependency graph
inside one Database operation and reuse calculated aggregates before a final
summary, without implementing financial arithmetic in GraphQL.

- Canonical relational, related and named-pipeline inputs reuse the existing
  permission binder, snapshot read batch, result shaping and encrypted cursors.
- Existing plain operations and legacy joined/related step configurations remain
  compatible; there is no schema migration. New advanced forms require Database
  0.14.0 and are checked at publish before replacing the serving release.
- Collections, dependencies, expressions and limits stay server-owned. Client
  inputs before an intermediate calculation require explicit `stage_params`;
  identity/access policies bind before source aggregation and joins.
- Named graph validation rejects writes, unknown dependencies, duplicate names
  and cycles. Database validates field types and executes bounded arithmetic.
- Realtime watches include all source scans, joins and permission collections.
  Database cardinality errors retain their structured GraphQL error code.

Acceptance coverage includes the 6000-cent remuneration fixture, duplicate
callers, foreign-centre exclusion, empty scopes, permission denial and paged
rates with viewer-bound cursors on SQLite, Pebble and AptevaDB. Existing
relational consumers and the complete module test/race suite are also checked.
