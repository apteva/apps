# GraphQL 0.4.2

Generic Tables relationships can now explicitly coerce a parent value to the
primitive type of the foreign-key column:

```json
{"relation":{"parent_key":"id","foreign_key":"prospect_id","value_type":"string"}}
```

Allowed `value_type` values are `string`, `number`, and `boolean`. Omitting it
preserves the existing same-type behavior. Invalid or impossible coercions fail
closed before a source read. This is resolver adapter configuration; schemas,
queries and variables remain ordinary GraphQL.

The change fixes schemas such as Flexylead where Tables' numeric internal row
ID is intentionally stored in a text foreign-key column. It applies uniformly
to nested `find`, `list`, `search`, `count`, and `aggregate` resolvers and keeps
the 0.4.1 guarded fast completion, batching, bounded parallel reads, source
adapters, authentication, pagination and aggregation behavior.

Verified with race-enabled tests and real isolated GraphQL + Tables 0.1.24
processes using text foreign keys, nested paginated reads and per-parent native
aggregates. Real Auth 0.12.0 and Functions 1.14.1 tests, Go vet and independent
build also pass. No Flexylead application source or data is modified.
