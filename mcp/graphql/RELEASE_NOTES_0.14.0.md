# GraphQL v0.14.0

Distinct access reads for `database_pipeline`. Requires Database >=0.6.5
(unchanged).

## Distinct access reads

Access reads fetched matching records and checked `max` before
deduplicating, so 1,000 sales in one authorised centre failed with
`source_result_truncated` even though they add a single centre.

`"distinct": true` on an access read now runs it as a Database aggregate
inside the same access batch:

    {"op": "aggregate", "args": {"collection": <configured>,
      "where": {"and": [<resolved filter>, {"field": <field>, "op": "is_not_null"}]},
      "groupBy": [<field>], "metrics": [{"name": "access_count", "op": "count"}],
      "limit": max + 1}}

- `max` bounds distinct values. `truncated: true` or more than `max` groups
  fail with `source_result_truncated`; a response without `rows` is a
  `source_error`. Database errors keep their codes. Nothing falls back to
  broader access.
- Collection, field and filter remain resolver configuration; the filter is
  only narrowed (null groups excluded).
- Requested values must still lie inside the distinct set; `optional` empty
  scopes behave as before.
- Rejected on publish: `distinct` that is not a boolean, `distinct` with
  `orderBy`/`limit`, and `distinct` on a `single_record` value (two profiles
  on one centre stay two records).
- Reads without `distinct` are unchanged.

## Verified

- Unit tests: operation shape, max and max+1 groups, truncation, malformed
  responses, Database errors, requested-value narrowing.
- Real Database 0.6.5: a centre policy (home centre UNION granted-offer sale
  centres, restricted by grant centres) with 2,500 sales in one centre,
  unrelated offers, exactly max / max+1 distinct centres, grant centre
  restriction, unknown and empty latest grants; `single_record` still counts
  duplicate profiles. The same test fails with `distinct: false`.
- The 22 external offers vectors still match.
