# GraphQL 0.4.0

Standard GraphQL execution and direct Tables relationships; independent from
the API app. No Flexylead-specific schema, SQL, expression language, or pipeline
engine was introduced.

- Fragments, aliases, merged fields, skip/include directives, strict typed
  inputs and variable defaults, explicit-null semantics, introspection,
  interfaces/unions, serial mutation roots, and proper field-error completion.
- Request-local Tables read deduplication and small-read batching, including
  nested list relationships. Large reads retain bounded parallel dispatch.
- Generic parent/foreign column mappings for nested Tables fields.
- `search` preserves Tables rows, totals, has_more, and scoped next_cursor.
- Query-only HTTP GET, POST, standard request extensions metadata, and
  locations/paths in field errors.
- Existing Auth/Function trust checks remain; no installed project or Function
  configuration was modified.

Verified with race-enabled unit tests and isolated real GraphQL + Tables
0.1.24 processes using 1,000 related rows. Nested loading used one root read and
one Tables batch for two parents; cursor continuation and skipped reads passed.
Real Auth 0.12.0 + Functions 1.14.1 regression tests also passed. Go vet/build and
the browser-module build passed. SDK remains pinned to the latest ancestor tag,
v0.82.0.

Compatibility: IDs now serialize as strings; nullable resolver failures can
return partial data; clients must check the GraphQL errors array. Relationship
mapping is not an authorization policy. Flexylead query/access adaptation is a
separate step and has not been deployed. This is core GraphQL support, not a
claim of complete support for every spec revision: @oneOf, interface inheritance,
incremental delivery, custom executable directives and graphql-transport-ws
remain outside this release. See README for configuration and boundaries.
