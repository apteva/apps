# GraphQL v0.16.0

Conditional query filters in `database_pipeline`, so an empty client selection
can mean "no filter" while an empty `in` keeps meaning "match nothing". No
Database change: conditions are resolved before anything is sent.

## Conditional filters

A child of a step or metric filter's `and` group may be:

    {"when": {"op": "gt", "args": [{"op": "length", "args": ["$param.centres"]}, {"const": 0}]},
     "filter": {"field": "centre_id", "op": "in", "value": "$param.centres"}}

- A false condition removes the child and a true one replaces it with its
  filter. Database never receives `when`, and if nothing is left the read has
  no `where`.
- An `and` group written empty in a config is left alone, so Database still
  refuses it rather than it turning into "no filter".
- The page and its `count_of` total resolve the same conditions with the same
  params. Subscriptions evaluate conditions and access values on every refresh.

## Authorization stays unconditional

- `$scope` and identity-sourced params are rejected in both `when` and `filter`.
- Conditional filters are rejected inside `or`, inside each other, at the root
  of a filter, in access reads and in plain `find` resolvers.
- Access expressions are unchanged and still take no client arguments.

## Strict conditions

- Conditions read `$param.<name>` and `{"const": ...}` with `length`, `eq`,
  `neq`, `lt`, `lte`, `gt`, `gte`, `and`, `or`, `not` and `coalesce`.
- Params must be fixed or a top-level `$args.<name>`, of type `string`,
  `number`, `boolean` or `exact_string_list`. `string_list` is refused, because
  dropping a blank could switch the filter off.
- Types are checked when the resolver is set, including a Boolean result.
  Nothing is decided by truthiness, unlike resolver modules: `"false"` is not a
  Boolean here.
- A null value fails the request unless `coalesce` handles it, whichever way the
  other operands came out.

## New param type: `exact_string_list`

Like `string_list` (a list of at most 1000 strings), but identifiers are kept as
sent; only exact duplicates are removed. `string_list` is unchanged. A
non-numeric value on an integer field fails with `invalid_argument`.

## Checked at publish

- Each argument a conditional filter reads must exist on the field, match the
  param's shape, and be non-null or have a default (`centres: [String!]! = []`,
  `status: String = ""`).
- Publishing reads one record selecting the conditional fields, so a mistyped
  field refuses the release even though a false condition would never have sent
  it. Aggregates with `lookups` or `buckets` skip this probe and any error
  surfaces when they run.
- Nested filters are validated in full, and search rules still apply inside them.

## Verified

- Unit tests: refused and accepted configs, evaluator types and null handling,
  binding, page and total in one batch, metric filters, exact lists, chunking of
  long selections.
- Real Database 0.7.0: the selection table with real access values, optional
  status, date and integer ID filters, search including `%`, paging and totals,
  publish refusals, and subscriptions following row edits and authorization
  changes.
