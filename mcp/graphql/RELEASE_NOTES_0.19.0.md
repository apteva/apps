# GraphQL v0.19.0

`database_pipeline` filters can now nest `exists` and `not_exists`, as Database
0.10.0 allows. Publishing a plan with a nested relationship used to fail with
`exists.where cannot hold another exists or not_exists`.

## Nested relationships

A clause's `where` can hold further clauses, so a permission can follow a chain
such as report to call to sale:

```json
{"exists": {"collection": "calls",
            "on": [{"localField": "call_ref", "foreignField": "local_id"}],
            "where": {"exists": {"collection": "sales",
                                 "on": [{"localField": "local_id", "foreignField": "call_id"}],
                                 "where": {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"}}}}}
```

- Up to 4 levels, and 8 relationships across the whole filter. GraphQL refuses
  anything past that when the plan is published.
- A nested `localField` reads from the record of the clause holding it.
- Every level needs an index on a related field.
- A page and its `count_of` total use the same resolved filter in one snapshot,
  and each record counts once.
- Subscriptions watch the collections at every level, so a change anywhere in the
  chain re-evaluates what's visible.
- A cursor is still refused once the resolved filter changes, and every page is
  authorised again.

## The rules are the same at every depth

- No client `when` and no text search inside a relationship's `where`.
- No client values under a `not_exists`, however deep, because negating a client
  filter would widen the result.
- Trusted `access_when` selection works at any level, and resolves before the
  query is sent.
- Relationships stay out of metric filters, access reads, client `when` filters
  and plain resolvers.
- An empty scope through a chain of `exists` answers empty with no read. A chain
  with a `not_exists` is sent to Database, because it can still match.

## Other fixes

- Publishing planned each nested clause on the step's own collection, where its
  fields don't exist, and its probe mangled a nested clause into a field filter.
  Each top-level relationship is now planned whole, with its values removed.
- An `access_when` on a field only the related collection has failed publication,
  because GraphQL looked for that field on the step's collection. This affected
  single-level relationships too, since 0.17.0. Related fields are now checked
  through their relationship.

## Requirements

- Nested relationships need Database 0.10.0. On an older Database, publishing such
  a plan is refused with a message that says so. Other plans keep working on
  0.8.0 and later.
- No changes for existing plans.

## Cost

Nested filters are expensive. Database rebuilds their key sets for each operation,
so a page and its total each pay, and it fails the query after about 30 s of read
work. Keep a selective top-level scope beside them where you can.
