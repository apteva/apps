# GraphQL v0.12.0

Protected Database reports without Functions: chained authorization lookups,
paging, and permission-based field masking inside `database_pipeline`.
Requires Database >=0.6.5 (unchanged).

## Chained access values

Access values can now depend on each other, so an authorization chain such as

    verified user -> latest enabled grant -> authorised offers ->
    related grids and permitted centres -> filtered records

is plain configuration:

- A read's `where` may reference an earlier value (`$scope.<name>`). Values
  run in dependency order, one Database read batch per level, for at most 4
  levels. Cycles and self-references are rejected on publish.
- `orderBy` and `limit` on an access read keep its first records ("the latest
  enabled grant") instead of failing past `max`. `limit` requires `orderBy`.
- Array fields are flattened: a field holding `["grid2", "grid3"]` contributes
  both values.
- `{"union": [...]}` and `{"intersect": [...]}` combine earlier values without
  a read.
- `single_record: true` requires exactly one matching record. `single: true`
  still counts distinct values, so two profiles on one centre pass `single`
  but fail `single_record`.
- `require_any_permission` on a value refuses callers holding none of the
  listed permissions.
- An empty value refuses the request before anything depending on it is read.

Each access level reads its own snapshot; report steps still share one.
Database sources keep using access values rather than `security.row_filters`
(Tables-only). Client params only ever appear as filter values, so they can
narrow the authorised scope but never widen it.

## Paging

A `find` step takes `"page": {"size": "$param.first", "cursor": "$param.after"}`:

- `size` is an integer or a number param. It defaults to `max_rows`, and
  larger requests are capped at `max_rows`.
- `cursor` is a string param holding Database's `nextCursor`.
- A paged step never fails on `hasMore`. Results can select
  `$steps.<id>.hasMore` and `$steps.<id>.nextCursor` (null on the last page).
- `{"op": "count", "count_of": "<find step>"}` counts the same collection and
  filter in the same snapshot, for "page N of total".

Paging uses Database cursors. Offset-based screens need to move to cursor
navigation.

## Field masking without Functions

- New expression op `in` (membership, with `eq`'s comparison), available in
  combine fields and resolver modules.
- `$identity.permissions` binds the verified permissions as a `string_list`
  param, and as a resolver-module input.
- Combine expressions can read `$param.<name>` and `$scope.<name>`.
- Combine `keep` copies a joined step's columns unchanged, so one field can be
  replaced for some callers.

Callers with `finance:read` get amounts; other permitted callers get the
same records with `amount: null`:

```json
"combine": {"view": {"join": ["rows"], "on": ["id"], "keep": ["rows"], "fields": {
  "amount": {"op": "if",
    "condition": {"op": "in", "args": [{"const": "finance:read"}, "$param.perms"]},
    "then": "rows.amount", "else": {"const": null}}}}}
```

## Validation

Full Go suite, race suite and vet with `GOWORK=off`. Unit tests cover:

- the full chain, including batch-per-level ordering, latest-grant arguments,
  flattened grids and intersected centres;
- refusals for two profiles, no grant, an empty intersection and a missing
  value permission, none of which read any records;
- graph validation (cycles, self-references, depth, malformed set operations);
- paging (size caps, cursors, last page, `count_of` filters);
- masking with and without the permission, and the `in` op.

`TestRealDatabaseAccessChains` runs against the released `database/v0.6.5` on
AptevaDB with 60 sales and the grant, offer, grid and profile chain above:

- The finance caller walks 3 pages of 4 and sees exactly the 10 authorised
  records with amounts.
- A caller without `finance:read` sees the same 10 with amounts null.
- A user with two profiles on one centre, and a user without `sales:read`, are
  refused with no data.

`TestRealDatabaseQueryOptions` and `TestRealDatabaseGuardedEdits` still pass.

SDK remains pinned to v0.99.0. No running installation or production
deployment is updated by this release.
