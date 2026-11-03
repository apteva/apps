# GraphQL v0.13.0

Optional and derived authorization scopes for `database_pipeline`, so a
policy such as "managers see the whole catalogue, partners see their latest
grant's offers, everyone else sees nothing" is plain configuration.
Requires Database >=0.6.5 (unchanged).

## Optional access values

`optional: true` lets an access value resolve to an empty scope instead of
refusing the request. Anything filtering on it then matches nothing: an empty
`in` is sent as a contradiction (`is_null` and `is_not_null`), never removed,
and finds and counts that can only match nothing are answered locally without
a Database call (aggregates are still sent). Failures still refuse optional
values: truncated reads, `single` / `single_record`, `require_any_permission`,
and Database errors.

## Expression values

`{"expr": ...}` derives a value from verified params and earlier values using
`if`, `and`, `or`, `not`, `in`, `length`, comparisons, `coalesce` and
`{"const": ...}`. It may reference `$scope.<value>` and `$param.<name>` for
params with a fixed value or an `$identity.` source; client `$args` are
rejected on publish, as are unknown operators, self-references and non-list
results at run time (`invalid_result`). `length` and `in` accept scope lists.

## Levels

`union`, `intersect` and `expr` values run in the level of their deepest
dependency and no longer consume one of the 4 read levels. Reads within a
level are still a single Database batch.

## Large `in` lists

`in` filters with more than 100 values (Database's per-`in` limit) are split
into `or` chunks of 100 instead of failing.

## Verified

- Unit tests for the offers policy (11 cases), optional failure codes, expr
  validation (14 rejected configs) and `in` normalisation.
- 22 external authorization vectors replayed unchanged on a real Database
  0.6.5 (`TestRealDatabasePolicyVectors`).
