# Separate SQL stages in one snapshot

GraphQL v0.10.0 adds `aggregate_pipeline` version 3, requiring Tables >=0.2.16.
Every stage runs as its own `tables_query` statement inside **one**
`tables_batch(mode: "read_snapshot")` call. Tables executes the statements on the
same read connection and SQLite transaction; each statement has an independent
SQL compilation budget. The Resolvers UI's stages template now uses version 3.
Existing version 1 single queries and version 2 relational CTEs remain compatible.

```json
{
  "version": 3,
  "engine": "tables_batch",
  "mode": "read_snapshot",
  "sources": ["records"],
  "final_stage": "summary",
  "result": "single",
  "max_rows": 1,
  "stages": [
    {
      "id": "facts",
      "columns": ["payload"],
      "sql": "SELECT json_object('total', COUNT(*)) FROM {records} WHERE tenant_id = ?",
      "params": [{"from": "$identity.tenant", "type": "string", "required": true}],
      "max_rows": 1,
      "max_bytes": 262144
    },
    {
      "id": "summary",
      "columns": ["total"],
      "sql": "SELECT json_extract(?, '$.total') + ?",
      "params": [
        {"from": "$stage.facts.rows.0.payload", "type": "json", "required": true},
        {"from": "$args.extra", "type": "number", "required": true}
      ],
      "max_rows": 1,
      "max_bytes": 1024
    }
  ]
}
```

Adapt table columns, identity predicates, and arguments to your own schema. The
runtime carries no financial SQL or business rules.

The fixed binding `$stage.<id>.rows.<index>.<column>` becomes a native Tables
parameter object `{"$ref":"<id>.rows.<index>.<column>"}`. The row index must be
canonical nonnegative decimal and below the referenced stage's declared row
budget; the column must be declared. Intermediate values must be scalars or
serialized JSON text. `type: "json"` validates the JSON text without double
encoding it. Other intermediate types are checked using the runtime's parameter
validator (SQL integer 0/1 is accepted for Boolean); any invalid value fails the entire field. The raw SQL value is bound
by Tables, so any normalization needed by subsequent SQL belongs in the fixed
SQL configuration. `required: false` allows SQL NULL; a missing row/column still
fails because Tables cannot resolve that reference. Stage results are unavailable
outside the batch. `{stage:id}` relational references are rejected in version 3.

SQL, stage IDs, dependency paths, columns, source declarations, final selection,
and budgets are immutable published resolver configuration. Client requests can
supply only values through `$args`, `$parent`, or verified `$identity` mappings.
Identity is taken exclusively from the authenticated request context. Request
values and JSON literals cannot inject dependency objects or SQL. Source checks
apply to every statement, including value-only stages with no table access.
Every table placeholder must be among the declared sources; Tables applies its
native read-only authorization to each statement. Implicit scoped sources and
automatic `row_filters` remain rejected, so mandatory verified identity predicates
must be explicit in the fixed SQL.

Stages may be declared in any order. Validation rejects unknown references,
cycles, unused stages, invalid paths, duplicate IDs/columns, relation references,
multiple statements, and ambiguous placeholders. Limits are 16 stages, 64 columns
per stage, 1,000 parameters per stage, and 64 KiB SQL **per statement including
the generated row-bound wrapper**. There is no 64 KiB limit on the combined graph.
Output columns are assigned by position from each stage's explicit column list.

Each statement is wrapped with a `max_rows + 1` bound to detect overflow. Stage
budgets default to 100 rows and 256 KiB of UTF-8 JSON row-object bytes; explicit
limits allow 1..10,000 rows and 1..4 MiB. GraphQL validates **all** returned stage
statuses, rows, byte budgets, truncation, and intermediate binding types before
accepting any final result. Any error, failed dependency, omitted result, native
truncation, or budget/type violation fails the entire field. There is no partial
success mode. Tables owns execution and may run downstream reads before GraphQL
validates stage budgets; all operations are read-only, and their output is discarded
on failure. Native Tables result/value/batch caps also bound intermediate work.
Stage byte budgets measure returned JSON row objects, not SQLite memory use.

The sum of declared stage row/byte budgets must fit the API release's `max_rows`
and `max_response_bytes`. They are reserved once per distinct loader execution
across the request, including aliases and list fanout; row budgets also contribute
to `max_cost`. Final GraphQL row/response limits continue to apply. Execution,
snapshot and native Tables query/batch deadlines and cancellation remain active.
There is no best-effort or multi-call fallback. Unrelated fields or resolver levels
do not share a request-wide Tables snapshot.

Tables returns intermediate envelopes to GraphQL for validation; **only the final
stage's result** enters GraphQL `rows`, `single`, or `envelope` output. Projection
metadata from every stage is merged, deduplicated and sanitized under the existing
metadata bounds, and appears in `extensions.sources`. It also appears in envelope
results. Intermediate data and private metadata never enter extensions/logs.

Read coalescing remains opt-in and keys the immutable API release, canonical
operation, variables and verified authorization (including identity, scope and
permission version). The loader key additionally includes the entire execution
plan, so differing budgets or parameter contracts never share shaped results.
Failures and partial executions are not cached across requests. Diagnostics retain
source/resolver timing, cancellation, and sharing details. Backend metrics count
one batch call and one read per separately executed SQL stage.

# Legacy version 2 relational stages

Version 2 remains compatible with existing published resolvers. It combines the
graph into a single statement and can hit SQLite's 65,535-reference compilation
limit. For large pipelines, migrate relations to bounded scalar/JSON parameters
and use version 3 above. Version 1 remains compatible.
Configure stages on a resolver, publish the immutable API release, and call the
ordinary typed GraphQL field. The Resolvers UI offers an editable stages template.

```json
{
  "version": 2,
  "engine": "tables_batch",
  "sources": ["records"],
  "final_stage": "summary",
  "result": "single",
  "max_rows": 1,
  "stages": [
    {
      "id": "eligible",
      "columns": ["id", "amount"],
      "sql": "SELECT id, amount FROM {records} WHERE tenant_id = ? AND occurred_at >= ?",
      "params": [
        {"from": "$identity.tenant", "type": "string"},
        {"from": "$args.start", "type": "datetime"}
      ],
      "max_rows": 500,
      "max_bytes": 262144
    },
    {
      "id": "summary",
      "columns": ["total", "amount"],
      "sql": "SELECT COUNT(*), COALESCE(SUM(amount), 0) FROM {stage:eligible}",
      "max_rows": 1,
      "max_bytes": 1024
    }
  ]
}
```

Adapt table columns, identity predicates and argument names to your own schema.
The runtime has no knowledge of tenants, records, or application policy beyond
the configured verified identity bindings and declared sources.

Stages contain fixed SELECT/WITH statements, anonymous `?` value parameters and
explicit output column names (assigned by position). `{table}` references a
declared source table; `{stage:id}` references another stage's relation. Use
SQL joins, subqueries, scalar selects or `IN (SELECT ...)` to consume its results.
Bindings accept the existing `$args`, `$parent` and verified `$identity` mappings
and literal values. SQL, identifiers, graph edges and final selection cannot come
from request parameters. References in SQL strings/comments are not graph edges.

The graph may be declared in any order. Unknown references, cycles, duplicate IDs,
unused stages and ambiguous parameter bindings fail validation. There are at most
16 stages, 64 columns per stage, 1000 total parameters and 64 KiB compiled SQL.
Stages cannot directly reference the reserved `gql_stage_` CTE identifiers.

GraphQL compiles the graph to materialized CTEs and makes **one**
`tables_batch(mode: "read_snapshot")` call containing one `tables_query` operation.
SQLite passes intermediate relations internally. The backend returns one final
JSON envelope and bounded stage budget checks, never intermediate row datasets.
GraphQL strips the checks and exposes the existing `rows`, `single`, or `envelope`
result contract. This requires Tables >=0.2.8; it needs no Tables changes and has
no fallback to separate calls or best-effort execution. A pipeline's stages share
one snapshot; unrelated fields/resolver levels still do not share a Tables
request-wide snapshot.

Each stage defaults to 100 rows and 256 KiB of UTF-8 JSON row-object bytes.
Explicit limits permit 1..10000 rows and 1..4 MiB. A bounded raw CTE collects at
most `max_rows + 1` rows, calculates row/byte usage, and gates its readable relation.
Over-budget relations contribute no rows to downstream stages; GraphQL fails the
field even when its final result is empty. SQL errors also fail the field. There
is no successful partial-intermediate fallback. JSON row encoding supports SQL
text, numeric and null values; convert SQL BLOB values explicitly.

The sum of declared stage row and byte budgets must fit the API release's
`max_rows` and `max_response_bytes`. These budgets are reserved once per distinct
loader execution across the whole request, including aliases and list fanout.
The row budget also contributes to `max_cost`. Final GraphQL row and response
limits continue to apply. Backend SQLite per-value limits, query/batch deadlines,
release execution deadlines and `max_snapshot_ms` further bound work. Stage byte
limits measure JSON row content, not SQLite's physical memory allocation.

Existing source declarations and Tables' native read-only SQL authorization apply
to the complete compiled query. Scoped source configurations and automatic
`row_filters` remain rejected; bind mandatory verified identity predicates in
your fixed SQL. Every contributing projection's metadata is read in the same
snapshot and sanitized into `extensions.sources`; envelope metadata is sanitized
too. Read sharing remains opt-in and keys the entire immutable release plus the
canonical operation, coerced variables and verified authorization context.
Failures and partial executions are never retained across requests.

Logs and dashboard diagnostics retain resolver/source timings and sharing details.
Backend metrics count one call and the number of logical SQL stages, rather than
claiming one physical query per stage.
