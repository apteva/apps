# GraphQL v0.10.0

Adds `aggregate_pipeline` version 3 to execute fixed dependent SQL stages as
separate statements inside one Tables read-snapshot transaction. This avoids the
SQLite 65,535-reference compilation limit caused by combining the entire graph
into materialized CTEs.

- Fixed `$stage.id.rows.0.column` mappings become native Tables `$ref` parameters.
  Stages exchange bounded scalar or JSON values; only the final result is exposed.
- Per-statement source checks and SQL/row/byte budgets, cumulative request limits,
  verified identity bindings, cancellation, sanitized merged projection metadata,
  and authorization-safe in-flight sharing remain enforced.
- Invalid, failed, missing or truncated stages fail the entire field. Resolver
  budgets and parameter contracts are included in loader memoization keys.
- The Resolvers UI template uses version 3. Existing version 1 single statements
  and version 2 relational stages remain compatible; large v2 graphs should migrate
  their relational edges to explicit value bindings.
- Requires Tables >=0.2.16 for dependent array references. SDK remains pinned to
  v0.99.0, the latest published main-line SDK tag checked for this release.

See STAGED_PIPELINES.md for the contract, migration and consistency boundaries.

Validation: SQLite compilation-limit regression; real Tables v0.2.16 integration
including concurrent-write snapshot consistency, intermediate failures and limits;
array-reference, authorization isolation, cancellation, concurrent sharing and
memoization tests; full Go race suite, vet/source build and Bun UI checks.

Only GraphQL is changed and released. No marketplace or running installation is
updated, and no production deployment is performed.
