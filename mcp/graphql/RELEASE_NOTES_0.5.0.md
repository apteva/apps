# GraphQL 0.5.0

- Accepts schema-defined typed GraphQL `where` input objects for Tables reads.
- Supports native scalar operators, nested AND, invertible NOT, and safe
  same-column equality OR lowering.
- Maps conventional `first`, `after`, and `includeTotal` arguments to native
  Tables pagination.
- Adds authenticated, non-overridable row filters derived from subject, tenant,
  or allowlisted Auth claims.
- Always combines configured, identity, client, and relationship filters; fixed
  source scope can no longer be replaced by client input.
- Pushes validated GraphQL field selections into Tables `select`, including
  fragment expansion and hidden relationship keys.
- Adds UI editing for identity row policies and filter-column mappings.
- Retains the standard GraphQL executor and guarded fast completion path; no
  query DSL, scripts, SQL, or Flexylead-specific execution behavior was added.
