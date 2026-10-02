# GraphQL v0.7.8

GraphQL v0.7.8 makes execution telemetry easy to inspect in the admin UI and
through MCP.

## Filterable request logs

- Adds server-side filters for duration, response bytes, returned rows, resolver
  count, HTTP status, operation name/type, and RFC3339 time windows.
- Adds stable sorting by newest, duration, response size, rows, resolvers,
  status, or operation name, with a bounded 500-row result set.
- Adds indexes for the heavy-query dimensions so slow and large requests remain
  fast to find as logs grow.
- Extends the `graphql_logs` MCP tool schema with the same filters and sorting
  controls exposed by the admin endpoint.

## Admin UI

- Adds quick filters for 100ms, 500ms, 1s, and 5s requests plus 100KB, 1MB,
  and 10MB responses.
- Adds operation/status filters, sort controls, server-side Apply/Clear actions,
  readable byte formatting, and visual highlighting for slow or heavy rows.
