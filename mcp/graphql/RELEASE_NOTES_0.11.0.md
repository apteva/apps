# GraphQL v0.11.0

Adds `database_pipeline`: a resolver operation that answers a whole report
from Database, without SQL, in one consistent snapshot. Requires Database
>=0.6.0 whenever Database sources are used.

## database_pipeline

A resolver on a `database` source declares a fixed plan with up to three
phases:

- **access** (optional): checks permissions from the verified Auth identity
  and resolves the values the caller may read (teams, centres, ...). Callers
  holding `unrestricted_if_permission` get what they requested, or a `default`
  read; everyone else gets an `otherwise` read (`single: true` requires
  exactly one value), narrowed to their request. Anything outside it fails
  with `permission_denied` before report data is read.
- **steps**: 1 to 100 `aggregate`, `find`, `count` or `get` operations sent to
  Database as one read-only `db_batch`, so every figure comes from the same
  snapshot. Database 0.6.0's `count_distinct`, metric filters, lookups and
  timezone buckets are available.
- **combine** (optional): joins grouped rows on their keys, appends a total
  row from ungrouped steps, and computes fields with resolver-module
  expressions.

`result` maps output fields to step results, combined rows or access values.

## Safety

- Collections, fields, metrics, groupings, lookups, buckets and ordering are
  resolver configuration. Clients only supply declared params (`$args`,
  `$parent`, verified `$identity`, fixed values), which may appear only as
  filter values, metric-filter values and `get` keys.
- Never partial: a step over its `max_rows` fails with
  `source_result_truncated`, Database budgets with `resource_limit`, and
  `timeout_ms` with `timeout`. A `find` step marked `top_n: true` returns its
  first `max_rows` records instead (for "latest N" lists).
- Integer counts and sums become JSON numbers while they stay exact (within
  2^53); larger values stay decimal strings.

## Subscriptions

A `Subscription` bound to a pipeline watches every collection the plan reads
(steps, lookup targets and access reads) through one Database watch. Each
commit re-runs the whole report once per subscriber group.

## Other changes

- New expression op `safe_divide` (resolver modules and combine fields): null
  when either value is null or the divisor is zero.
- The Database dependency is now `>=0.6.0`. Database 0.5.3 could also return
  wrong counts on AptevaDB.

## Validation

Full Go suite, race suite and vet with `GOWORK=off`. Unit tests cover plan
validation (client-chosen fields, collections, limits and unknown references
are rejected), access outcomes, snapshot batching, combine and totals, every
failure class and watch derivation.

`TestRealDatabasePipeline` runs against the released `database/v0.6.0` on
AptevaDB with 50,000 calls and 5,556 sales. Every figure equals an independent
Go computation, including distinct and conditional counts, Paris day buckets
across the October DST change and a chained lookup. Access refusals return no
data. GraphQL adds about 5 ms over replaying the same 7-operation `db_batch`
directly (524 vs 519 ms on a loaded machine). A sale commit updates two
subscribers with a single batch. Report cost is Database's: a chained lookup
and an unindexed latest-5 sort took most of the batch time.

SDK remains pinned to v0.99.0; Tables >=0.2.16 is unchanged. No running
installation or production deployment is updated by this release.
