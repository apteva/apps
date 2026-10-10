# Fixed dependent SQL stages

GraphQL v0.9.0 adds `aggregate_pipeline` version 2. Version 1 remains compatible.
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
