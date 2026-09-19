# GraphQL 0.5.1

- Adds generic bounded ordered distinct selection for Tables `find` and `list`
  resolvers, supporting “latest row per group” fields with standard GraphQL.
- Supports up to eight `distinct_by` columns, optional scalar
  `distinct_defaults`, and an explicit scan bound up to 1,000 rows.
- Applies GraphQL `first`/`limit` after distinct selection and automatically
  includes hidden distinct keys in Tables projection pushdown.
- Rejects distinct configuration for pagination envelopes because post-scan
  totals/cursors would otherwise be ambiguous.
- Includes unit, race, real Tables sidecar and projection coverage. No scripts,
  SQL query language, or application-specific resolver behavior was added.
