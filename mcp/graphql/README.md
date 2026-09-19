# GraphQL app

The standalone `graphql` app owns GraphQL schemas, resolver bindings,
aggregation-aware source adapters, and realtime subscriptions. It is
deliberately independent from the REST/API gateway app.

## Minimal setup

Create and publish a schema:

```json
{
  "sdl": "type Query { orderCount: Int! }",
  "environment": "staging"
}
```

Bind `Query.orderCount` to a Database source:

```json
{
  "name": "orders",
  "kind": "database",
  "config": {"database": "staging", "collection": "orders"}
}
```

```json
{
  "parent_type": "Query",
  "field_name": "orderCount",
  "source": "orders",
  "operation": "count"
}
```

The public endpoint is the app's own `/graphql` route. Queries use the
published schema for the selected server-side environment; clients cannot
choose an arbitrary database or source.

The first implementation intentionally keeps public routes behind the normal
Apteva app-token gate. A follow-up auth layer will add project-bound API keys
and Auth/JWT policies before exposing a custom hostname to untrusted clients.

## Aggregation

Use `operation: "aggregate"` with a Database or Tables source and configure
`groupBy`/`group_by` and `metrics`. The adapter delegates aggregation to the
native source instead of scanning records in the GraphQL process.

## Execution performance (0.2.2)

Independent root query fields execute concurrently (up to eight); mutation
fields remain ordered. Compiled schemas and validated documents are cached,
and resolver/source bindings are loaded once per request with a bounded
one-second metadata cache. Scalar row projections avoid per-cell metadata
lookups. Query results themselves are not cached.

Small Tables read fan-outs use `tables_batch` (requires Tables 0.1.22 or newer).
Reads whose summed requested limits exceed 500 use parallel individual calls.
Rows-only queries skip total counts unless explicitly configured otherwise.
Batching uses `best_effort`, not cross-table snapshot consistency.

The release pins the latest published SDK, v0.81.0. Unreleased SDK app-call
batch/inner-result changes are not required or included.

## Realtime transport

`/realtime` speaks the `graphql-transport-ws` framing. A subscription resolver
can set `config.topic`; Tables row events are bridged automatically and trusted
source adapters or the `graphql_event_publish` tool can publish matching
events. Database-native change-feed integration is the next step for full
collection CDC delivery.
