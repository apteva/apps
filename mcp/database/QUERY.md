# Composable read queries

Available in Database 0.13.0. `db_query` (MCP) and
`POST /operations/query` (trusted admin HTTP) execute a typed graph of relations
on one read snapshot in one database. The operation requires `database.read`,
including a read app grant when a calling app uses `scope: "project"`.

```json
{
  "database": "reporting",
  "version": 1,
  "nodes": [
    {"name":"source","op":"scan","collection":"sales",
     "where":{"field":"centre","op":"eq","value":"a"},
     "select":["centre","contracts","rate_cents"]},
    {"name":"amounts","op":"compute","from":"source","computed":[
      {"name":"amount_cents","type":"integer","expr":{
        "op":"multiply","args":[{"field":"contracts"},{"field":"rate_cents"}]
      }}]},
    {"name":"totals","op":"aggregate","from":"amounts","groupBy":["centre"],
     "metrics":[{"name":"amount_cents","op":"sum","field":"amount_cents"}]}
  ],
  "result":{"from":"totals","orderBy":[{"field":"centre"}]},
  "timeoutMs":30000
}
```

The response is `{rows:[...], truncated:false}`. Integer fields and counts are
int64 decimal strings. Result overflow is an error, never a partial answer.
The complete [dependent-aggregation fixture](testdata/dependent-aggregation.json)
calculates counts per centre and period, computes rates, joins them back to
sales and sums individual amounts. Its [records](testdata/dependent-records.json)
include duplicate callers and a foreign centre: September pays 4000 cents and
October pays 2000 cents, totalling 6000, rather than the pooled-rate 4000.

## Nodes and dependencies

Every node has a unique lowercase `name`, an `op`, and optional `maxRows`.
`from` and `relation` always reference node names, never stored collections.
Forward references are supported. All nodes, including unused nodes, are
validated before records are read. Unknown names, cycles, unknown fields,
invalid types and irrelevant arguments fail validation.

- `scan`: `collection`, optional `where` and `select`. Only this node names
  stored collections. It streams every matching record, with no page limit.
  The normal indexed filters and `exists` language apply. Selecting metadata
  explicitly carries it into later relation schemas.
- `compute`: `from`, optional `computed`, `where`, `predicate`, `select`.
  Computed definitions run in order and may reference earlier computed fields.
  `where` uses normal field filters on the computed row. `predicate` is a
  boolean expression; only true keeps a row. Both precede projection. This node
  can be used just for filtering or projection.
- `join`: `from`, optional root `as` (default `root`), `joins`, optional `where`
  and `select`. Each join is `{relation, as, kind?, on, cardinality?, where?}`.
  `on` contains 1–8 `{local:"alias.field", foreign:"field", comparison?}` pairs
  combined with AND. Local fields may use earlier joins. Comparison semantics
  are those of relational reads: one type by default, exact decimal `text`, or
  local text/integer to foreign integer `numeric`. Kinds are `inner` (default),
  `left`, `semi` and `anti`. Null keys never match; empty strings do. A join's
  `where` filters the right input before matching, preserving unmatched left
  rows for left joins. Top-level `where` filters the joined row. Right fields
  are named `as.field`; outputs are flat records with those names.
- `aggregate`: `from`, `metrics`, optional `groupBy` and `where`. Uses the
  existing typed accumulators, conditional metrics and exact distinct counts.
  Ungrouped empty input gives one row: counts are `"0"`, sums are null. Grouped
  empty input gives no rows. No public aggregate page limit is applied to an
  intermediate result. Joins and date calculations can be supplied in upstream
  nodes; aggregate-node lookups, buckets and derived clauses are not accepted.

`cardinality: "many_to_one"` checks that eligible non-null right keys are unique.
A duplicate fails with `cardinality_violation`, even if that key is not probed
by a left row. Omit it for bounded one-to-many joins. A right key may have at
most 10,000 rows. Semi/anti joins add no right fields.

## Numeric expressions

The existing `field`, `const`, `coalesce`, text/JSON and filter-based `if` forms
remain available. Numeric expressions add `{op,args}`:

| Operators | Arguments and result |
| --- | --- |
| `add`, `multiply`, `min`, `max` | 2–16 numeric expressions |
| `subtract`, `safe_divide` | Two numeric expressions |
| `eq`, `ne`, `gt`, `gte`, `lt`, `lte` | Two numeric expressions; boolean result |
| `if` | Boolean condition, then expression, else expression; chosen branch only |
| `and`, `or`, `not` | Boolean expressions; short-circuit, SQL-style nulls |
| `round` | One numeric expression, plus `round: 0..12` and optional `roundMode` |

Numeric string constants are exact decimal literals in this context. Arithmetic
rejects text fields; there is no implicit numeric parsing of stored text.
Integer add/subtract/multiply results are checked at each operator. Constants
must also fit int64 when integral. Overflow fails with `resource_limit`.
Expression nesting is at most eight levels, with at most 1024 expression nodes
across a query. Rational intermediates are bounded to 4096 numerator/denominator
bits; decimal literals are limited to 128 characters and three exponent digits.

Arithmetic and comparisons propagate null. Safe division returns null for a
zero denominator. A null `if` condition selects its explicit else branch.
`min`/`max` propagate null too; use `coalesce` where a fallback is intended.

Division stays rational inside the expression tree. A fractional expression
requires an explicit computed `type`: `integer` requires an integral int64
result (use `round` when needed); `number` explicitly converts to finite binary
floating point. Publishing a ratio as a `number` before using it later gives
that field floating-point semantics. Keep financial amounts in integer cents
and compare unrounded ratios within the expression when exactness matters.

Rounding defaults to `half_away_from_zero`, matching the existing final derived
division's rounding direction. Other modes are `half_even`, `floor`, `ceil` and
`trunc`. Round at the intended node: SUM(round(each sale)) can differ from
round(SUM(sales)), and the executor preserves that order. Existing `derived`
division responses continue to use their existing floating-point format.

Numeric expressions also work in `computed` on `joined_aggregate`,
`joined_page`, branches and rollups, through the same checked evaluator.

## Scope and execution limits

Database validates authenticated project/app access and applies supplied filters.
GraphQL/CRM must derive the user policy and put eligibility filters on **every
independent scan**, including inputs used for distinct counts and joins. Reusing
a filtered relation preserves its membership. Rate periods and display windows
are separate caller decisions: filtering calls to a displayed day before
computing the period rate can produce the wrong amount. The query accepts no
cross-database nodes, mutations, raw SQL, network calls or permission-policy code.

The executor uses the manager's single read transaction, query deadline and
native query admission. Scans and computations stream. Shared relations and
right join inputs are materialized once per request; joins use bounded in-memory
hash tables. Aggregates reuse the portable streaming accumulators on all three
adapters. Temporary relations exist only in process memory. Native query-plan
pushdown and disk spill are not implemented for this operation.

| Limit | Default / maximum |
| --- | --- |
| Nodes | 32 |
| Fields per relation | 512 |
| Computed fields per compute node / joins per join node | 32 / 8 |
| Emitted rows per node | 100,000 |
| Accounted live retained memory per plan | 128 MiB |
| Aggregate accumulator state per active aggregate | At most 16 MiB, within plan memory |
| Shared row/probe work units | 16,777,216 |
| Final rows | 1000 |
| Final encoded payload | 4 MiB |
| Deadline | 30 seconds, configurable to at most 60 seconds |

`limits: {intermediateRows, memoryBytes, work}` may lower the shared server
limits; zero/omitted keeps the default. A node's `maxRows` may lower its own
row bound. `result.limit` lowers the final row bound (zero/omitted means 1000).
These bounds fail the whole query rather than truncating it. Memory accounting
includes retained shared rows, aggregate state, join indexes, active rows and
results; it is an estimate, not an RSS cap. Filters can scan more stored
candidates than they emit; adapter cancellation and the shared deadline still
apply. Errors identify the failing node. Expensive work returns
`query_too_expensive`; row, memory, result and arithmetic limits return
`resource_limit`. No partial result is returned.

A query may also be an operation in a read-only `batch`, sharing that batch's
snapshot and deadline. Its plan has its own budgets, as existing batch reads do.
