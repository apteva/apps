# GraphQL v0.9.0

Adds `aggregate_pipeline` version 2 for fixed dependent SQL stages. GraphQL
compiles the immutable graph to materialized CTEs and executes it through one
Tables read-snapshot batch. Intermediate relations stay inside SQLite; only the
final rows, stage budget checks and projection metadata return to GraphQL.

- Typed request/parent/verified identity bindings, dependency validation and
  explicit final-stage selection. Existing version 1 pipelines remain supported.
- Per-stage and request-wide row/JSON-byte budgets, execution/snapshot deadlines,
  cancellation and fail-closed intermediate stages.
- Existing source authorization, sanitized projection metadata, request-local
  deduplication and authorization-safe in-flight sharing remain in effect.
- Resolvers UI includes a dependent-stages template and configuration guidance.
- SDK pinned to v0.99.0, the latest published main-line tag. Tables >=0.2.8 required.

See STAGED_PIPELINES.md for the contract and consistency boundaries.

Validation: full Go race suite, vet and source build with GOWORK=off; released
Tables v0.2.8 and v0.2.15 integration; SQL graph/authorization/budget/sharing/
cancellation tests; Bun telemetry tests and browser verification of the template.

Only GraphQL source is changed and released. Tables, marketplace registry,
production deployments and running installations are not changed.
