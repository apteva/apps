# GraphQL v0.7.9

GraphQL v0.7.9 makes errors and request performance easy to inspect in the Logs
panel and a configurable dashboard widget. It builds on v0.7.8's server-side
telemetry filters.

## Request health UI

- One-click Errors, Slow, and Large views, with search, rolling/custom time
  windows, environment, error code, operation, HTTP status, numeric bounds, and
  sorting controls.
- Errors include partial GraphQL failures returned with HTTP 200.
- Expand requests to read every retained error, its field path and source
  location, request ID, authorization scope, API release, execution phases,
  source timings, row/resolver counts, and response size. Copy details exports
  the stored metadata.
- Summary counts, averages, maximum duration, and response bytes cover every
  matching stored request independently of the visible list limit.

## Dashboard widget

- Exports GraphQL request health (`graphql-telemetry`) in `dashboard.home`, using
  the same telemetry view and filters as the Logs panel.
- Project-scoped widget supports half/full sizes and settings for API slug,
  time window, slow threshold, initial view, and list limit.
- API selection, request details, and optional 30-second live refresh are
  available inside the widget.

## HTTP logging

- Logs malformed requests, authentication and method rejections, and sanitized
  handler failures when a project is known.
- Duration spans authentication, request decoding, execution, and response
  writing. Public/internal handler nesting produces one log per request.
- Adds environment, full error details, execution phases, and indexes for time
  window queries; preserves existing logs through an additive migration.
- Query text, variables, request bodies, and authentication tokens are not
  stored. Logs remain asynchronous and may drop entries on overload or storage
  failure. WebSocket subscription telemetry is outside this request view.
- Pins the latest published SDK on the main release line, v0.95.0.

## Validation

- Go test suite, focused race checks, and go vet passed.
- Bun telemetry tests and browser verification of the panel/widget passed.
- GraphQL source build passed with GOWORK=off against the published SDK.

This release publishes source and marketplace metadata only; it does not
upgrade or restart running installations.
