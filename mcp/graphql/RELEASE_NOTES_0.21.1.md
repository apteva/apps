# GraphQL v0.21.1

The first release with literal search. Version 0.21.0 was withdrawn: it was
published on the wrong commit and removed before anything installed it.

Pipelines can now search with `contains_ascii`, Database's literal substring
filter, so a search for `100%` finds a real percent sign. It can also run where
`like_contains_ascii` can't: across several fields in an `or`, and on linked
records inside an `exists`. Existing plans behave as before.

## The filter

```json
{"field": "label", "op": "contains_ascii", "value": "$param.q"}
```

The resolver owns the field and operator. The value is a literal, a fixed string
param or a top-level string argument, and is sent exactly as the client wrote it.
`like_contains_ascii` is unchanged and keeps its wildcards.

## Optional search over linked records

An empty value matches every record, so a search inside an `or` or an `exists`
must sit in a client `when` that reads its value:

```json
"where": {"and": [
  {"field": "centre_id", "op": "in", "value": "$scope.centres"},
  {"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"or": [
    {"field": "source_name", "op": "contains_ascii", "value": "$param.q"},
    {"exists": {"collection": "sales", "on": [{"localField": "sale_ref", "foreignField": "local_id"}],
      "where": {"and": [
        {"field": "centre_id", "op": "in", "value": "$scope.centres"},
        {"or": [
          {"field": "contract", "op": "contains_ascii", "value": "$param.q"},
          {"field": "client_name", "op": "contains_ascii", "value": "$param.q"}]}]}}}]}}]}
```

- A blank `q` removes the whole group, so a report with no linked records still
  appears under its scope.
- The page and its `count_of` total send the same filter.
- If a `when` still lets a blank through, the request fails. It never matches
  everything.
- Inside the search group, a branch of an `or` can be a client `when`, for example
  a phone search that needs six characters. A false one drops that alternative.

## Search can't replace authorization

- The mandatory scope stays outside the `or`. An `or` holding a search is refused
  if it reads `$scope` or an identity param at its own level, or holds a
  relationship that doesn't search.
- Inside a searched `exists`, `$scope`, identity params and `access_when` still
  work, so linked records keep their own checks. A match on a linked record that
  fails them doesn't affect rows or totals.
- A client `when` still can't hold a relationship that doesn't search, or any
  `not_exists`.
- Client values stay out of `not_exists` at any depth, and access reads can't
  search.

## Publishing

Publishing validates each search with its operator on the collection that owns
the field, through the relationship for related fields. A missing or non-text
field, or a Database without `contains_ascii`, refuses the release. Search
arguments found inside relationships must be `String!` or have a default.

## Limits

- `contains_ascii` needs Database 0.11.0. Older versions refuse a plan that uses
  it at publish, and other plans keep running on Database 0.8.0 or later.
- A filter can now hold 24 relationships, up from 8, on Database 0.11.1. Optional
  ones count, because one request can use them all, so a selected permission and
  its search share the limit. A plan with more than 8 is refused at publish on an
  older Database.
- `contains_ascii` never selects an index, so keep an indexed scope beside it on
  large collections.
- It compares one stored text field at a time, so it can't search across two
  fields, inside a JSON value, or on a normalised phone number unless one is stored.
