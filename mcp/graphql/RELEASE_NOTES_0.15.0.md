# GraphQL v0.15.0

Multi-field text search in `database_pipeline`, using the predicate Database
0.7.0 added. Requires Database >=0.7.0.

## Text search

A step or metric filter may now hold:

    {"fields": ["resource_key", "opportunity_key", "commercial_id"],
     "op": "like_contains_ascii", "value": "$param.q"}

- Fields and op are resolver configuration. The value is a literal, a fixed
  string param, or a top-level string `$args.<name>`; `$scope`, `$identity`,
  `$parent` and nested arguments are rejected.
- The predicate reaches Database unchanged, so its semantics are Database's:
  the fields are joined by one space, `%` and `_` stay wildcards, ASCII case
  folds, and an empty value matches every record.
- Because an empty search matches everything, a search can only narrow: it is
  rejected inside `or` and in access reads, so it always sits beside the
  resolved scope. With an empty scope nothing is read, whatever is searched.
- A `count_of` total uses the page's filter in the page's snapshot.

## Checked at publish

- Filter operators must be ones Database supports. Pipelines are re-validated
  at run time, so a stored plan with an unknown operator now fails with a
  clearer error; Database already refused those plans.
- A search argument must be `String!` or have a default such as
  `q: String = ""`.
- Database validates each search before the release is written, so a missing
  field, a non-text field or an older Database refuses the release. Aggregates
  with `lookups` or `buckets` may search derived fields, so they skip this
  probe and any error surfaces when they run.

## Verified

- Unit tests: 24 rejected configs, accepted shapes, unchanged binding, page and
  total sharing one batch and one filter.
- Real Database 0.7.0: publish refusals, matching across joined fields,
  wildcards, default and explicit empty searches, paging totals, the 1024-byte
  limit, and a `%` search staying inside the caller's scope.
- Real Database 0.6.5: publishing a search is refused with a clear message.
