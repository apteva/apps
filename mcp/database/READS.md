# Core reads: find and aggregate

Available in Database 0.14.0. New callers use these nine
record operations: `get`, `find`, `insert`, `update`, `upsert`, `delete`, `count`,
`aggregate` and `batch`. MCP names add `db_`; trusted HTTP calls use
`POST /operations/<operation>`. Database/collection/index management, diagnostics, grants and
realtime subscription controls remain separate administration/streaming APIs.

`find` returns records. `aggregate` calculates summaries. Joins, related reads,
computed values and dependent pipelines are capabilities of those operations.
Ordinary requests keep their existing native execution paths and response formats.

## Aggregate

The ordinary request is unchanged:

```json
{"database":"reporting","collection":"sales","groupBy":["centre"],
 "metrics":[{"name":"revenue_cents","op":"sum","field":"amount_cents"}]}
```

For joined aggregation, send the former `joined_aggregate` arguments to
`aggregate`: `as`, `joins`, `computed`, `filter`, `distinct`, `pick`, `branches`,
`rollup`, `groupBy`, `metrics` and `derived`. Field names use `alias.field`, with
`root` as the default alias. Existing inner/left/semi/anti/aggregate join kinds,
`on`/`anyOf`, comparisons, physical-record deduplication, first/latest selection,
branch unions and rollups retain their existing semantics.

Lookups and timezone buckets still work on ordinary aggregates. They can also
enrich the final input of relational and pipeline aggregates: use the local
field's actual name, such as `root.owner_id` or `sale.at`. Enrichment runs after
relational input/rollup stages and before final grouping and metric filters.
A relational top-level `where` belongs to the stored root; its `filter` runs
before final enrichment. Use metric `where` to condition a metric on an enriched
field, or pipeline terminal `where` to filter the entire enriched relation.

### Dependent calculations

Add `pipeline: {version: 1, nodes: [...], from: "relation_name"}` when an earlier
aggregate must feed a calculation or join. The node definitions are the same
scan/compute/join/aggregate definitions introduced in 0.13.0; see
[the stage and numeric expression reference](QUERY.md#nodes-and-dependencies).
These are internal stages of one operation, not additional MCP tools.

```json
{
 "database":"reporting",
 "pipeline":{
  "version":1,
  "nodes":[
   {"name":"sales","op":"scan","collection":"sales",
    "where":{"field":"centre","op":"eq","value":"a"}},
   {"name":"amounts","op":"compute","from":"sales","computed":[
    {"name":"amount_cents","type":"integer","expr":{"op":"multiply",
     "args":[{"field":"contracts"},{"field":"rate_cents"}]}}
   ]}
  ],
  "from":"amounts"
 },
 "groupBy":["centre"],
 "metrics":[{"name":"revenue_cents","op":"sum","field":"amount_cents"}]
}
```

The pipeline produces input rows; top-level `where`, `groupBy`, `metrics`,
`orderBy`, `limit` and `derived` operate on those rows. `metrics` is always
required. Lookups and buckets are optional final enrichment. Use collection-based
input, branches, or a pipeline as one source; do not mix those input forms or put
relational shorthand fields beside `pipeline`. Put input computations and joins
inside the pipeline instead.

The [full acceptance request](testdata/dependent-aggregate.json) computes
per-period rates, joins them back to sales and returns 6000 cents for the
[synthetic September/October fixture](testdata/dependent-records.json). The same
request works on SQLite, Pebble and AptevaDB.

Every aggregate returns `{rows: [...], truncated: boolean}`. `limit` only limits
final groups, after all eligible input has contributed. Each returned metric is
complete for its group. Intermediate row/group/work/memory overflow fails the
whole operation; it never silently produces partial totals. Ungrouped empty input
returns one row with count `"0"` and sum null; grouped empty input returns no rows.
Counts and integer sums retain exact int64 decimal-string encoding. Round where
business rules require it; no optimizer moves rounding across aggregation.

## Find

Ordinary `collection`, `where`, `select`, `orderBy`, `limit`, `cursor`,
`requireIndex` and nested `lookups` keep their existing behavior and plain field
names. `count: true` adds an exact `total` before pagination. `offset` up to
100000 is supported, but cannot be combined with a cursor. Large stored offsets
may exceed the adapter's read buffer; use continuation cursors for large scans.

For joined paging, send the former `joined_page` arguments to `find`: `as`,
`joins`, `computed`, `filter`, `distinct`, `pick`, `group`, ordering and paging.
These requests use `alias.field` names and return the same `records` (or
`groups`), exact item `total`, `hasMore` and `nextCursor` as the old operation.
Old joined-page cursors resume through `find` and vice versa. Grouped paging
counts one group as one item, preserving deduplication, order and complete ties.

For bounded related records, send the former `related_read` arguments to `find`:
`collection`, `relation`, optional `match`, `count`, ordering and paging. Related
joins/computations/filters belong inside `relation.sources`. The result keeps
plain parent fields with related rows nested under `relation.as`. Source ordering
and limits are applied before `match`, so an omitted related row cannot match.
Old related-read cursors also resume through `find`. `requireIndex` applies to
ordinary stored-collection reads; relational/related requests enforce their
join-index rules and reject that additional flag.

For calculated records, use the same `pipeline` input as aggregate, then apply
top-level `where`, `select`, `orderBy`, `limit`, `offset`/`cursor` and `count`.
`find` returns `{records, hasMore, nextCursor}` and, with `count: true`, `total`.
It does not accept final metrics; put an intermediate aggregation in the pipeline
or use `aggregate` for a summary. Put pipeline lookups/joins in named input nodes.

Pipeline paging sorts on the requested fields and uses row-content identity plus
a duplicate ordinal to break ties deterministically, including identical rows.
Pipeline find currently retains all matching output rows within the plan's
memory budget before sorting and paging. Default
ordering is deterministic content order. Cursors are signed, expire after one
hour, and bind to the normalized plan, filters, ordering, projection, page size
and consulted collection schema versions. Changing schemas or the plan invalidates
the cursor. Each page opens a fresh snapshot, as ordinary find does; data changes
can move records between pages. Pagination never supplies a partial group to a
later aggregate because terminal paging happens only after the input plan runs.

## Permissions, limits and compatibility

Both operations require `database.read`, including a project read grant when a
calling app uses `scope: "project"`. Authenticated project/app namespaces and
caller-supplied eligibility filters are unchanged. Apply authorization filters on
every independent scan before joins, counts and aggregation. CRM/GraphQL derive
the user policy and business expressions; Database executes the whole request
inside one read snapshot. Separate calls do not share a snapshot; use a read
`batch` for multiple requests that must agree.

Pipeline limits and the 30-second shared deadline (maximum 60 seconds) are those
of 0.13.0: 32 input nodes, 100000 emitted rows per node, 128 MiB accounted plan
memory, at most 16 MiB per aggregate accumulator, and a 4 MiB response. `limits`
can lower intermediate rows, memory or work. Final `find`/`aggregate` defaults
remain 100 rows/groups, with a maximum of 1000. Pipeline reads do not create an
absent default database. Resource exhaustion returns an error without a result.
Memory accounting is an estimate, not a process RSS cap.

| Legacy name | Canonical call |
| --- | --- |
| `joined_aggregate` | `aggregate` with the same arguments |
| `joined_page` | `find` with the same arguments |
| `related_read` | `find` with the same arguments |
| `query` | `aggregate` for summaries or `find` for calculated records, with `pipeline` |

Legacy HTTP endpoints, MCP tools, batch operation names and their response,
error and cursor contracts remain supported. They are marked as compatibility
aliases in tool descriptions and MCP `_meta` (`deprecated`, `replacement`).
They remain callable and discoverable during migration so existing agents and
apps keep working. New documentation and examples use the nine canonical verbs.
The old `query` endpoint keeps its strict final-overflow behavior and
`{rows,truncated:false}` response; canonical `find` pages and `aggregate` limits
only final groups. Ambiguous combinations and unknown fields are rejected rather
than ignored. No storage migration or adapter format change is required.

## Bounded related-parent aggregates (unreleased)

`aggregate` can use the same `relation` and `match` input as `find`. Use identical
parent scopes, relation source definitions and match filters for a list and its
summary. Eligibility filters, deduplication and ordering run before the single
context cap shared by all sources in source order. `match` tests only retained
entries. Each matching parent feeds aggregate metrics once, regardless of how
many context entries match. Without `match`, every scoped parent participates,
including parents with no context.

Parent fields may be named directly or as `<as>.field` (`root` by default).
Parent-level `computed` expressions use the existing relational expression syntax;
their names must not shadow stored parent fields. `groupBy`, conditional metrics,
checked arithmetic, nulls, distinct counts and final `derived` expressions retain
their ordinary semantics. `orderBy` and `limit` apply to final groups, with the
usual `truncated` flag. A sum or average over no non-null values is null.

A related aggregate rejects parent paging (`cursor`, `offset`, `select`, `count`
and `group`) and top-level joins, filter, distinct, pick, branches, rollup,
lookups, buckets and requireIndex. Put related-row transformations inside
`relation.sources`. Pipelines and relation inputs remain alternative input forms.
The operation projects the parent fields it needs, streams parents, accounts for
related lookup buffers while decoding them and shares retained-memory/work limits
with aggregate state. Cancellation and resource failures return no partial result.

See [the executable request fixture](testdata/related-aggregate.json) and
`engine/related_aggregate_test.go`. The fixture returns two matching parents,
60 points and a 50% average, while an out-of-cap or ineligible entry matches none.
A read-only `batch` can contain the corresponding `find` and `aggregate` so a page,
exact total and summary share a snapshot. Callers still derive permission policy
and supply scope/eligibility filters; Database executes those supplied rules.
