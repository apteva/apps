# Database 0.13.0

Adds `db_query` (MCP) and `POST /operations/query`: named scan, compute, join and
aggregate relations executed in one database read snapshot. An aggregate can
feed a computation, be joined back to individual facts, and feed another
aggregate without writing temporary business records. Business rules remain
caller-supplied expressions.

## Contract

- Typed graph with version 1, named nodes and a selected result. Forward
  references work; duplicate names, missing dependencies, cycles, unknown fields
  and incompatible types fail validation before data is scanned.
- Shared deadline, row/work/memory limits, bounded relation joins and explicit
  `many_to_one` cardinality checks. Intermediate and final overflows fail the
  whole query rather than returning partial or silently truncated answers.
- Checked numeric computed expressions: add, subtract, multiply, safe division,
  comparisons, conditions, scalar min/max and explicit rounding. Division stays
  rational inside expressions; money can remain int64 cents. Numeric expressions
  also work in the existing relational computed fields.
- Existing authenticated project/app namespaces and read grants apply. Callers
  derive user authorization policy and supply eligibility filters on each scan.
- Read-only queries can share a read batch. A query never creates an absent
  default database.

See [QUERY.md](../QUERY.md) for schemas, expression semantics, limits and examples.

## Verification

- Full `GOWORK=off go test ./...` and `go test -race ./...`, vet and standalone
  build passed on macOS during implementation.
- SQLite, Pebble and AptevaDB all pass the dependent-aggregation fixture:
  September's two unique callers and one contract produce 4000 cents; October's
  four callers and one contract produce 2000 cents; per-sale aggregation returns
  6000 cents rather than the pooled-rate 4000.
- Coverage includes alternate grouping dimensions, display filters separated
  from rate periods, foreign-centre exclusion, duplicate callers, empty and
  zero-caller periods, int64 boundaries, explicit rounding order, duplicate join
  keys, 1201 intermediate groups, budget failures and no partial results.
- A real backend commit between independent rate and sales scans leaves the
  in-flight answer at 6000 cents; the next snapshot sees the new sale and its
  recalculated 12000-cent total.
- The same fixture passes over MCP and HTTP with a read-only app grant. The
  compiled sidecar exercises the new operation on all three adapters.

## Compatibility and limits

The request graph uses a bounded portable executor on all three adapters.
Native pushdown and spill are not part of this operation. Default bounds are
32 nodes, 100000 emitted rows per node, 128 MiB accounted plan memory, 16 MiB per
aggregate accumulator, 1000 final rows, 4 MiB final payload and a 30-second
shared deadline (up to 60 seconds). Memory accounting is not an RSS cap.

There is no new storage migration or native format change. The app-sdk pin is
v0.115.0, the latest published tag by commit topology at release preparation.
GraphQL resolver integration is a separate consumer change.
