# GraphQL v0.17.0

Permissions that depend on related records can now be compiled into a mandatory
Database predicate, so a plan no longer needs to list eligible record ids first.
Relational filters require Database 0.8.0, and the manifest minimum moves to
`>=0.8.0`. Plans without them behave as before and still run on Database 0.7.0.

## Relational filters

`database_pipeline` step filters accept Database's `exists` and `not_exists`:

    {"exists": {"collection": "sales",
                "anyOf": [{"on": [{"localField": "local_id", "foreignField": "call_id"}]}],
                "where": {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"}}}

- Collections, fields and comparison modes are configuration. Clients can only
  supply ordinary filter values.
- Access values and params bind into `where`, with long lists chunked. `not_in`
  is new, and an empty one matches every non-null value.
- `where` can't hold a nested relationship, a client `when` or a text search.
  `not_exists.where` can't hold client values, because negating a client filter
  would widen the result.
- A filter holds at most 8 relationships, counted across all of its branches.
  They are rejected in metric filters, access reads and client `when` filters.

## Selection by trusted values: `access_when`

A child of an `and` group may be `{"access_when": <condition>, "filter": ...}`
when a permission applies only to some users.

- Conditions and filters read only access values, identity params and fixed
  params. Client params and `$args` are refused. `when` stays client-only and
  can't hold relationships, so a client can't decide whether a restriction
  applies.
- Conditions gain `in` and can read `$scope`. Types are checked when the resolver
  is set, and a missing identity list fails the request.
- `access_when` is rejected inside `or` and at the root of a filter.

## Publication

Publishing asks Database to plan every relationship with its values removed, on
both sides of any selection. An unknown collection or field, types that can't be
compared, a related field with no index and an older Database all refuse the
release. Bound value types can't be checked at that point, so a value that doesn't
fit a field still fails at query time.

## Totals, subscriptions and cursors

- `count_of` reuses the page's whole resolved filter, relationships included, in
  the same snapshot.
- Subscriptions watch every related collection, including ones behind a selection,
  so sale, call, grant, profile and offer changes re-evaluate visibility.
- A cursor is valid only for the resolved filter that produced it. A changed grant
  or client filter refuses it, restoring them makes it valid again, and every page
  is authorised again.

## Security fix

A plain resolver's client `where` argument reached Database as written, so with
Database 0.8.0 an `exists` in it could test records in a collection the resolver
doesn't name. It is now refused in any letter case, and plain resolver configs
refuse relationships and conditional filters when published.

## Cost

Each `on` list needs an indexed related field, and relationships cost far more
than an indexed scope. Database's guidance is to keep an indexed equality scope
beside them. On AptevaDB, aggregates with these filters run in one stream rather
than the parallel pipeline.
