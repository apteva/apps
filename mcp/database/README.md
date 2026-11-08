# Database

Independent Apteva sidecar app, version 0.15.2. SQLite, Pebble and the optional experimental AptevaDB adapter implement one local record API. The app owns its files and does not change CRM, Tables, or other apps' databases.

[Release notes](docs/release-0.15.2.md) · [Realtime API](REALTIME.md)

## Working in this version

- Several named databases per authenticated project or calling-app scope, with SQLite as the default and an explicit adapter for each database.
- SQLite collections use a typed, `WITHOUT ROWID` storage layout with native composite primary keys. Existing v1 collections remain readable and migrate atomically on their first write through a shadow-table swap.
- SQLite uses a serialized writer with persistent prepared statements, a separate four-connection read-only pool, and checkpoint work amortized through a larger WAL threshold. The `durable` profile is the default; the opt-in `balanced` profile uses `synchronous=NORMAL` with a wider crash-loss window.
- SQLite bulk inserts use compiled multi-row statements inside the same atomic transaction, reducing per-record statement overhead without weakening durable commits.
- Database routing and immutable collection metadata are cached within each process, keyed by authenticated scope. Schema writes invalidate the caches; SQLite point reads reuse prepared statements and Pebble point reads use a single atomic lookup.
- Several independently defined collections in every database.
- Typed records, UUID defaults, composite primary keys, exact int64 values encoded as decimal strings, and service-owned timestamps/versions.
- `get`, `find`, `insert`, `update`, `delete`, `upsert`, `count`, `aggregate`, `batch`, and `explain`.
- Relational reads: `related_read`, `joined_page` and `joined_aggregate` over indexed joins.
- Dashboard aggregates: distinct counts, per-metric filters, many-to-one lookups and timezone calendar buckets; read batches answered from one snapshot.
- Generic single-field/compound, ascending/descending, and unique indexes. Native SQLite and AptevaDB compound indexes; ordered secondary and uniqueness keys in Pebble.
- Atomic record writes and batches across collections in one database; optimistic version checks. Pebble automatically groups concurrent record writes into shared durable commits.
- Filtering, projection, stable ordering, and signed cursor pagination. A cursor expires after one hour and becomes invalid after an index/schema change. Pages observe fresh snapshots; changing sort keys between pages can move records.
- MCP tools, an authenticated admin HTTP API, and a dashboard for database/collection creation, browsing, inserts, queries, aggregation, and indexes.
- Project isolation, grant checks on MCP, and separate private namespaces for authenticated calling apps.

All three adapters are embedded and local to the Apteva host. Choosing an adapter preserves the record and aggregate request format.

AptevaDB pins `github.com/apteva/db v0.2.0-alpha.15` and uses synchronous writes with full flushes by default. Create a new database with `{"database":"native","adapter":"aptevadb"}`, or select **AptevaDB (experimental)** in the panel. Its private directory belongs to one app process. Existing SQLite and Pebble databases keep their adapters; changing backends requires an explicit export/import. Ungrouped row counts use native `CountDocuments`; covered scalar equality predicates count index entries without loading documents. Counts with staged writes retain transaction-aware evaluation, and field counts retain non-null semantics.

The native format remains 10 (no new migration from Database 0.5.0): opening an older native database permanently upgrades it, so back up native data with its original binary before installing this build. The app does not mount standalone directories.

The adapter stores typed scalar documents and encodes JSON fields as opaque JSON strings to preserve exact nested numbers and the app’s JSON depth contract. Native compound indexes provide ordered reads and nullable uniqueness. Atomic native batches plus a snapshot/write overlay implement cross-collection writes and read-your-writes. Collection/index changes use a durable intent journal so restart completes schema publication before accepting queries. Compatible broad aggregates use the native parallel streaming pipeline with up to four workers by default; indexed filtered aggregates keep their streaming index path. Native AVG and non-null field counts now use alpha.4 reducers. Integer SUM uses native exact reduction with per-group range proofs; out-of-int64 totals fail with `resource_limit`, and uncertain intermediate sums rescan the same snapshot through the checked accumulator. Staged write overlays keep that accumulator and decode only required fields into reusable query-local storage. The projected reader validates the pinned codec and uses the public decoder for other layouts. Reads default to a 16 MiB retained results/sort/group budget; projected sorts stream into a bounded top-page heap.

AptevaDB tuning is available through app configuration or environment variables (app configuration takes precedence):

| App configuration | Environment | Default | Accepted range |
|---|---|---:|---|
| `aptevadb_sync_mode` | `DATABASE_APTEVADB_SYNC_MODE` | full | full, barrier, os |
| `aptevadb_cache_mib` | `DATABASE_APTEVADB_CACHE_MIB` | 64 | 0 (explicit uncapped opt-out) or 1–1024 MiB |
| `aptevadb_mapped_cache_mib` | `DATABASE_APTEVADB_MAPPED_CACHE_MIB` | 64 | 0 (disabled), or 1–8192 MiB |
| `aptevadb_managed_memory_mib` | `DATABASE_APTEVADB_MANAGED_MEMORY_MIB` | 0 (disabled) | 0, or 1–8192 MiB |
| `aptevadb_aggregate_disk_mib` | `DATABASE_APTEVADB_AGGREGATE_DISK_MIB` | 1024 | 0 (spill disabled), or 1–8192 MiB |
| `aptevadb_max_groups` | `DATABASE_APTEVADB_MAX_GROUPS` | 1,048,576 | 1–16,777,216 |
| `aptevadb_total_cache_mib` | `DATABASE_APTEVADB_TOTAL_CACHE_MIB` | max(256 MiB, one cache) | 0 (auto), or 1–8192 MiB; must cover one cache |
| `aptevadb_memtable_mib` | `DATABASE_APTEVADB_MEMTABLE_MIB` | 0 (disabled) | 0–256 MiB |
| `aptevadb_max_concurrent_queries` | `DATABASE_APTEVADB_MAX_CONCURRENT_QUERIES` | 2 | 1–64 |
| `aptevadb_aggregate_workers` | `DATABASE_APTEVADB_AGGREGATE_WORKERS` | min(4, GOMAXPROCS); panel default 4 | 1–64 |
| `aptevadb_query_memory_mib` | `DATABASE_APTEVADB_QUERY_MEMORY_MIB` | 16 | 1–256 MiB |

The total cache setting bounds the sum of configured native cache targets across open handles. Least-recently-used idle native handles are closed without deleting their registry entries or files, and reopen on demand. Active requests and registered watches pin their handles; if all possible eviction candidates are pinned, opening another native database returns `resource_limit`. Explicit per-database cache=0 opts out of cache-target admission. File mappings, WAL, snapshots and other native allocations remain outside this target, so it is not an RSS cap. SQLite and Pebble handles are unchanged.

Write buffering is separately opt-in: setting a finite cache no longer implicitly enables the engine's default half-cache memtable. Query slots are shared across native databases and acquired before creating read snapshots; queue time counts toward the query deadline. At most 64 additional queries may wait; excess requests return `resource_limit`. Point reads and watch polling without initial snapshots bypass this queue. The per-query 16 MiB work budget is unchanged.

Complete-primary-key `COUNT(*)` groups use an ordered record query: each group has count one, including when the input is filtered. This preserves empty results, composite-key ordering, write overlays and the extra result used to report truncation. Partial-key groups and field counts keep aggregate evaluation. Count-only aggregates push their ordered result window into the native pipeline, preserving sort precedence and keeping only the requested output page; this does not remove the native hash group's memory requirement. Numeric aggregates retain validation across all groups so pagination cannot hide overflow. When native working state or materialized results exceed their budget, the adapter retries on the same snapshot through the native owned-result stream, with optional disk spill. It validates every completed group and retains only the best limit+1 rows. The stream reserves three quarters of the work budget for the native executor and one quarter for retained adapter results. The parallel executor remains in place for aggregates that fit, including filtered aggregates, whose filter is passed as a leading `$match` so the native planner can shard an index range or full scan; staged writes and uncertain integer reductions retain their checked portable fallback. Disk and group guards return resource_limit, with no partial response.

Flush modes apply when the app mounts; restart/remount it to apply a changed setting to all owned native databases. `full` requests a drive-cache flush on macOS. `os` requests ordinary `fsync`, like SQLite WAL/FULL with `fullfsync=OFF`; `barrier` orders writes without forcing the drive cache to stable media. OS and barrier provide weaker power-loss protection on macOS. On other supported systems the modes use `fsync`. Every exposed mode keeps native `Sync: true`; there is no async option. `explain` and startup logs report the effective mode. For a SQLite-comparable ordinary-flush test, select `aptevadb_sync_mode: os` or set:

```sh
DATABASE_APTEVADB_SYNC_MODE=os
```

See [matched flush-profile measurements](benchmarks/FLUSH-PROFILES.md). Choosing the same requested flush primitive does not establish identical crash behavior across different engine/WAL implementations.

Cold mapped translations are reclaimed periodically toward a 64 MiB per-database soft target by default; zero disables this policy. Recently used mappings survive a sweep. An optional managed-buffer target adds native admission/backpressure for owned buffers and query reservations; it defaults to disabled. Neither target caps process RSS. `database_describe` returns on-demand native `memory` diagnostics: current process RSS, mapped residency estimates, file-page-cache residency, private managed buffers, pinned storage, query reservations, targets and reclaimed translations. These categories overlap and must not be summed.

Aggregate scratch stays below the owned native database directory in `aggregate-tmp`. The default 1 GiB quota is per active query and includes simultaneous merge inputs/output; app-wide query admission therefore also bounds the number of concurrent scratch users. Scratch is removed on success, cancellation and errors; opening the database cleans abandoned native execution leases after a crash. Zero scratch quota disables disk use but still permits an ordered stream that fits. The group guard remains explicit and configurable; increasing it does not increase memory, scratch, response or deadline allowances.

See [alpha.4 adapter/direct-driver comparisons](benchmarks/APTEVADB-ALPHA4.md) and the [earlier cache experiment](benchmarks/ADAPTER-AGGREGATE-OPTIMIZATION.md) for historical configurations. Current write buffering is separately opt-in; finite cache targets no longer implicitly enable memtables. SQLite and Pebble settings are unchanged.

## Build and run

Use Go 1.25.1 or newer. This module pins app-sdk v0.115.0, the latest published tag by commit topology at release preparation.

```sh
cd apps/mcp/database
GOWORK=off go build -o /tmp/apteva-database .
APTEVA_DATA_DIR=/tmp/apteva-database-data \
DB_PATH=/tmp/apteva-database-data/sdk.db \
APTEVA_APP_TOKEN=local-development-token \
APTEVA_APP_PORT=8098 \
/tmp/apteva-database
```

Use `GOWORK=off` for release validation so the published SDK pin is tested rather than a local workspace overlay.

The SDK starts `/health` and `/mcp`, authenticates app routes, and serves `ui/`. The manager stores its registry and managed databases below `ctx.DataDir()/database/`. Do not share this directory between multiple running app processes.

Build the dashboard panel from the apps repo:

```sh
bun run scripts/build-panels.ts --app database
```

## Realtime

AptevaDB supports opt-in native capture, resumable `db_changes` polls and `POST /realtime/watch` SSE. The dashboard can enable realtime and refresh the selected collection and submitted aggregates after commits. SQLite/Pebble capture is a later phase. See [API, permissions and limits](REALTIME.md) and [performance checks](benchmarks/REALTIME.md).

## Example: multiple databases and collections

Call `db_database_create` twice:

```json
{"database":"sales","adapter":"sqlite"}
```

```json
{"database":"archive","adapter":"pebble"}
```

```json
{"database":"native","adapter":"aptevadb"}
```

Call `db_collection_create` for each desired collection, such as `orders` and `customers`, within either database:

```json
{
  "database":"sales",
  "collection":"orders",
  "fields":[
    {"name":"country","type":"text"},
    {"name":"amount","type":"number"},
    {"name":"status","type":"text"}
  ]
}
```

An omitted primary key creates a text `id` field with generated UUIDs. A declared `primaryKey` array can instead select one or several non-null scalar fields. Supported field types are `text`, `integer`, `number`, `boolean`, `datetime`, and `json`. Undeclared fields are rejected. Omitted nullable fields become null; other fields must be supplied. Field defaults and schema alteration are not implemented yet.

Call `db_insert`:

```json
{
  "database":"sales",
  "collection":"orders",
  "records":[
    {"country":"ES","amount":100,"status":"paid"},
    {"country":"ES","amount":50,"status":"paid"},
    {"country":"FR","amount":90,"status":"pending"}
  ]
}
```

Call `db_index_create`:

```json
{
  "database":"sales",
  "collection":"orders",
  "index":{
    "name":"by_status_country",
    "fields":[
      {"field":"status","direction":"asc"},
      {"field":"country","direction":"asc"}
    ],
    "unique":false
  }
}
```

Call `db_aggregate`:

```json
{
  "database":"sales",
  "collection":"orders",
  "where":{"field":"status","op":"eq","value":"paid"},
  "groupBy":["country"],
  "metrics":[
    {"name":"orders","op":"count"},
    {"name":"revenue","op":"sum","field":"amount"},
    {"name":"average_order","op":"avg","field":"amount"}
  ],
  "orderBy":[{"field":"revenue","direction":"desc"}]
}
```

Result:

```json
{"rows":[{"country":"ES","orders":"2","revenue":150,"average_order":75}],"truncated":false}
```

Changing `database` to a Pebble database with that schema and data executes the same contract. Aggregates operate on all matching input within work limits, not a page of input records. An output limit is applied after grouping and sorting; `truncated` only refers to output groups. SQLite uses native SQL aggregation; Pebble reduces filtered ordered-index scans.

`count` with no field counts records. `count` with a field counts non-null values; other metrics skip null. Empty ungrouped input yields one row with zero counts and null other metrics. Empty grouped input yields no rows. Integer sums/counts are decimal strings and integer overflow fails; averages and floating sums are approximate. `min`/`max` support scalar fields, while `sum`/`avg` require numeric fields.

### Dashboard aggregates

Four optional extensions keep dashboard queries in one aggregate pass, with no new operations:

```json
{
  "database":"calls",
  "collection":"calls",
  "lookups":[{"as":"campaign","collection":"campaigns","localField":"campaign_id","select":["name","channel"]}],
  "buckets":[{"name":"day","field":"started_at","unit":"day","timezone":"Europe/Paris"}],
  "where":{"field":"campaign.channel","op":"eq","value":"ads"},
  "groupBy":["day","campaign.name"],
  "metrics":[
    {"name":"calls","op":"count"},
    {"name":"callers","op":"count_distinct","field":"caller_id"},
    {"name":"sales","op":"count","where":{"field":"status","op":"eq","value":"sale"}},
    {"name":"revenue","op":"sum","field":"amount","where":{"field":"status","op":"eq","value":"sale"}}
  ],
  "orderBy":[{"field":"day","direction":"asc"}]
}
```

- **`count_distinct`** counts distinct non-null values of a scalar field (or of a bucket).
- **Metric `where`** restricts one metric to matching rows, so conditional counts and sums need no second query. It uses the normal filter language.
- **`lookups`** (at most 4) attach the one record of another collection in the same database whose single-field primary key equals `localField`. The join is many-to-one, so it never multiplies counted rows. Its fields are addressed as `as.field` in `where`, `groupBy`, metrics and metric filters; null and dangling references give null fields. A lookup's `localField` may be an earlier lookup's field. `localField` and the target key must have the same type.
- **`buckets`** (at most 4) derive `hour`, `day`, `week` (`weekStart` `monday` or `sunday`), `month` or `year` keys from a datetime in an IANA timezone (default UTC). Calendar units return the local start date such as `2026-03-23`; `hour` returns the UTC instant at which the local hour starts, which stays unambiguous across daylight-saving changes and for half-hour offsets. Timezone data is compiled in.

SQLite runs `count_distinct` and metric filters as native `COUNT(DISTINCT …)` and `FILTER (WHERE …)`.

AptevaDB (alpha.15) runs all four natively, in its parallel pipeline and its bounded stream:
- filters on stored fields form a leading `$match`, which reads through an index when one applies, including a compound index whose leading fields the filter binds by equality or `in` lists (one index range per value combination, at most 64), optionally with a range on the next field (alpha.12 added compound indexes, alpha.13 `in` lists);
- each lookup becomes `$lookup` on the target's primary-key field, followed by `$unwind` that keeps unmatched rows;
- filters on joined fields form a second `$match`;
- buckets become `$dateTrunc` group keys;
- metrics become filtered `$count`, `$sum`, … and `$countDistinct` accumulators. Distinct counts are exact: when one group's distinct sets outgrow the work budget (several filtered `count_distinct` metrics without `groupBy`, for example), alpha.14 counts disjoint slices of the values in separate passes over the spilled state.

Alpha.11 bounds a lookup's hash table by rows, not bytes, so the join may hold at most one target row per 512 bytes of work budget (32,768 rows at 16 MiB). A larger target falls back to the portable path.

Everything else uses the portable streaming accumulator on the request's snapshot:
- SQLite requests with lookups or buckets;
- every extended request on Pebble;
- on AptevaDB, buckets used outside `groupBy`, staged writes, and lookups past the row cap.

In the portable path, filters on stored fields are pushed to the adapter's scan and the rest run after joining. Joined records are read by primary key and cached within the query's work budget.

`find` accepts the same `lookups` and nests each referenced record under its `as` name (null when absent). Filtering and ordering stay on the collection's own fields, and `select` must include each `localField`.

## Core read operations (Database 0.14.0)

Use the nine record operations `get`, `find`, `insert`, `update`, `upsert`,
`delete`, `count`, `aggregate` and `batch`. `find` supports joins, grouped pages,
bounded related rows and calculated pipeline records. `aggregate` supports
ordinary summaries, joined aggregation and dependent pipelines with top-level
final metrics. See [the canonical contracts and migration guide](READS.md) and
[the 6000-cent acceptance request](testdata/dependent-aggregate.json).

Ordinary reads retain their native paths. Legacy specialised operation names
remain supported as marked compatibility aliases while callers migrate.

## Relational reads

`find` and `aggregate` read joined rows inside one snapshot, on every adapter, and can share a read `batch`. The legacy `related_read`, `joined_page` and `joined_aggregate` names below are compatibility aliases; their request details also apply to the canonical operations. Database applies the filters it is given. Who may see what remains the caller's decision, so pass the eligibility filters you need.

Joins name earlier fields as `alias.field`; the root's alias is `as`, default `root`.

```json
{"as":"sale","collection":"sales","kind":"left","anyOf":[
  {"on":[{"local":"root.sale_ref","foreign":"local_id"},{"local":"root.centre","foreign":"centre"}]},
  {"on":[{"local":"root.sale_ref","foreign":"id","comparison":"numeric"},{"local":"root.centre","foreign":"centre"}]}],
 "where":{"field":"state","op":"eq","value":"valid"}}
```

- `kind` is `inner` (default), `left`, `semi`, `anti` or `aggregate`. Semi and anti joins filter without adding fields or rows. An aggregate join takes `metrics` and adds each one as `as.metric`, computed over that row's related records, for example `{as:"reach",kind:"aggregate",on:[...],metrics:[{name:"advisers",op:"count_distinct",field:"adviser"}]}` then `filter:{field:"reach.advisers",op:"eq",value:"1"}`. A join's `where` belongs to the join, so a left join keeps its row when no related record satisfies it.
- Comparison `""` needs one type on both sides. `"text"` compares exact decimal text: `"1"` matches 1, `"01"` does not. `"numeric"` reads the local text as an integer: `"01"` matches 1.
- A related field of each alternative needs an index, or the read fails with `query_too_expensive`. One row matches a physical record once, however many alternatives find it.
- `computed` adds named values: `field`, `const`, `coalesce`, `nullIfEmpty`, `trim`, `strip`+`chars`, `concat`+`separator`, `json`+`path`, `if`+`then`+`else`. A value that does not fit its declared `type` fails the read.
- `filter` runs over joined rows. `distinct` keeps the first row per combination of aliases (physical records) or fields. `pick {by,orderBy,where}` then keeps the first qualifying row per key; use `direction:"desc"` for the latest.

**`related_read`** returns parents with their related rows. `relation {as,limit,sources}` fills one shared `limit`, source by source. Each source applies eligibility, orders its rows with the physical key as tie-break, and takes what still fits; `match` then tests only those rows. A row past the limit can never match. Each parent returns the exact rows under `as`, with a `source` field, and `count:true` adds an exact `total`.

**`joined_page`** returns `total`, `hasMore`, `nextCursor` and either `records` or, with `group {by,orderBy:[{field,direction,agg}]}`, `groups:[{key,rows}]`. One row or one group is one item, and `total` counts items before paging. It reads the whole joined input for each page, so bound it with `where`. Use `cursor`, or `offset` up to 100000. Until the page is chosen it keeps only each item's order values and which rows belong to it. It then reads just the page's rows on the same snapshot, so memory follows how many items match and how many rows the page shows, not how wide the rows are.

On AptevaDB the page reads the matching root records once, in whichever order costs least. With `distinct` it reads them in primary key order instead, because the first row `distinct` sees wins. That takes batches of 1,000, each of which has to fit `aptevadb_query_memory_mib`, and if the filter's index can't return primary key order, every batch reads all the matches again, so a large `distinct` page behind such an index can reach the 30-second limit. SQLite and Pebble always read in batches.

**`joined_aggregate`** takes the `groupBy` and `metrics` of `aggregate` and returns `rows` and `truncated`. Integer sums stay exact, `count` is `"0"` over no rows, and `sum` is null. `derived [{name,op:"divide",numerator,denominator,round?}]` is null on a null side or a zero denominator.

Two more stages answer questions one grouping cannot. `branches [{collection,as,where,joins,computed,filter,distinct,pick,select}]` replaces `collection` and concatenates inputs that select the same names and types; a top-level `distinct` of those names removes what several branches found. `rollup {by,metrics,computed,filter}` aggregates per key first, and the outer `groupBy` and `metrics` then read one row per key. For example, roll up per sale with `count_distinct` and `min` of its campaign, compute `if` one campaign `then` that campaign `else` a constant, then count sales per computed campaign.

Each read has a work budget and a 128 MiB memory budget, and exceeding either fails the read rather than returning partial rows. The memory budget is an accounting estimate, not a cap on process memory. In `joined_page` an item costs about 500 bytes plus its share of what else the read holds: each join can cache up to 16 MiB, a `distinct` remembers each key it has seen, and `pick` keeps each winner's order values until the page is chosen. A page's own rows count too, so one group with too many rows fails even when the page holds only that group.

## Dependent pipelines

Use `aggregate` with `pipeline: {version: 1, nodes, from}` and the usual final
`groupBy` and `metrics`. Use `find` with the same pipeline to page calculated
records. Aggregate outputs can be computed and joined back to another relation
before the final calculation, all in one read snapshot without temporary business
records. See [READS.md](READS.md). The old `query`/`db_query` operation remains a
compatibility entry point; [QUERY.md](QUERY.md) retains the stage/expression reference.

## Query and mutation rules

Filters use `{field,op,value}` leaves and explicit `{and:[...]}` / `{or:[...]}` groups. Operators: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in`, `not_in`, `is_null`, `is_not_null`, `starts_with`, `contains_ascii`. Null predicates omit `value`. `in` takes 1–100 non-null values; a one-value list is treated as `eq`, so it binds index prefixes on every adapter. `not_in` takes the same list with SQL's null behaviour: a null value does not satisfy it. String matching is literal, case-sensitive UTF-8 byte comparison.

For a literal substring in one text field use `{field:"label", op:"contains_ascii", value:"100%"}`. It matches `instr(lower(coalesce(label,'')), lower(value)) > 0`: `%`, `_` and `\` are ordinary characters, ASCII letters compare case-insensitively (other letters exactly), and nulls read as `""`. An empty value matches every record, including nulls. The field must be `text` and the value a string of at most 1024 bytes. It never selects an index, so keep an indexed scope beside it on large collections. It works in `find`, `count`, filtered aggregates and `exists` clauses.

Text search is one filter node over several text fields: `{fields:["resource_key","opportunity_key","commercial_id"], op:"like_contains_ascii", value:"23 op"}`. Nulls read as `""` and the fields are joined in the given order with one space; the result matches `"%" + value + "%"` with SQLite's default `LIKE`: `%` and `_` stay wildcards (`_` is one UTF-8 character), ASCII letters compare case-insensitively (other letters exactly), backslash has no escape meaning, and whitespace is kept. An empty value matches every record and is dropped from the plan, so it costs nothing. 1–16 existing `text` fields, a string value of at most 1024 bytes. It combines with `and`/`or` and works in `find`, `count`, `aggregate`, metric `where` and read batches, before pagination and aggregation. Every adapter evaluates it natively (AptevaDB `$like`, SQLite `LIKE`) and none can use an index for it, so keep the indexed scope (tenant, date range) beside it: the planner still narrows through those, and the search reads only the records in that range.

`exists` and `not_exists` test related records of another collection in the same database without returning them. Customers in one region with a paid order and no refunded order:

```json
{"and": [
  {"field": "region", "op": "eq", "value": "emea"},
  {"exists": {"collection": "orders",
    "on": [{"localField": "id", "foreignField": "customer_id"}],
    "where": {"field": "status", "op": "eq", "value": "paid"}}},
  {"not_exists": {"collection": "orders",
    "on": [{"localField": "id", "foreignField": "customer_id"}],
    "where": {"field": "status", "op": "eq", "value": "refunded"}}}
]}
```

- `on` works like SQL's `ON a AND b`: a record is related when every pair matches (1–8 pairs), so composite keys are `[{"localField":"tenant_id","foreignField":"tenant_id"},{"localField":"id","foreignField":"customer_id"}]`.
- `anyOf` replaces `on` when a record can be related in several ways, like `ON a OR b`. Each entry has its own `on` list (1–16 entries): `"anyOf": [{"on": [{"localField":"id","foreignField":"customer_id"}]}, {"on": [{"localField":"email","foreignField":"billing_email"}]}]`. A clause takes exactly one of `on` and `anyOf`.
- A null local or foreign value never matches; empty strings do, as in SQL. Without `comparison` both fields must have one type. `comparison: "text"` compares text and integer fields by decimal text with no trimming or other coercion, so `"007"` does not match the integer 7.
- `where` applies to the related record; the same related record must satisfy it and the match. Nodes combine with `and`/`or` and can nest inside a `where`, up to 4 levels and 24 nodes across the whole filter. A nested node reads its `localField` from its immediate parent: in `customers → orders → payments`, the second relationship reads fields of `calls`, and each level needs its own index. Each record still counts once. Nested filters get a work budget of about 30 s of reads; past it the operation fails with `query_too_expensive`, and any failure inside a node, `not_exists` included, fails the whole operation.
- Each `on` list needs one `foreignField` with an index that starts with it (or the primary key); otherwise the request fails with `query_too_expensive` naming the fields. Add indexes before relying on a relationship. In a composite key, index the most selective field.
- Supported in `where` for `find`, `count`, `aggregate`, `explain` and read batches, evaluated before sorting, pagination and aggregation, on the operation's snapshot. Each record counts once. Metric `where`, `update` and `delete` reject them. Any evaluation failure fails the request; no partial result is returned.
- SQLite compiles a correlated subquery. Pebble and AptevaDB start with index probes and, on wide scans, switch to one bounded pass over the related collection that builds key sets (up to the 16 MiB work budget, else they keep probing). When an index covers the clause's `where`, the pass reads only the records that index selects, so index the fields a relationship filters on. On AptevaDB aggregates with these filters run in one stream rather than the parallel pipeline, so keep an indexed equality scope beside them.

A multi-value `in` on the leading fields of an index, with an optional range on the next field, reads a union of index ranges: at most 64 value combinations and 1 MiB of bounds, past which the filter still applies over a scan. Each record counts once. On AptevaDB, a count covered by one index reads index entries alone, without fetching documents; filters that need documents, such as text search or `exists`, read only the records in those ranges when they name at most a quarter of the collection. A paged `find` chooses between the union and an ordered stream by counting matches from the index. Pebble keeps an ordered stream when one exists, unless `requireIndex` needs the union.

`count` takes `diagnostics: true` to report what actually ran:

```json
{"count": "3356", "diagnostics": {"executed": true, "planSource": "execution", "strategy": "native-count",
  "plan": {"access": "index-count", "index": "by_region_at", "indexOnly": true, "equalityPrefix": 1, "ranges": 2},
  "indexOnly": true, "documentFetches": 0, "indexEntriesExamined": 3356, "examined": 3356, "returned": 3356,
  "peakMemoryBytes": 212, "queueMs": 0.01, "snapshotMs": 0.02, "executionMs": 0.03}}
```

`exists` and `not_exists` add `{"probes", "keySetsBuilt", "relatedRowsRead", "relatedDocumentsExamined", "keySetBytes"}`. Only AptevaDB reports engine statistics; Pebble and SQLite give timings and the planned access, marked `"planSource": "explain"`. A count inside a read batch omits `queueMs` and `snapshotMs`, which the batch shares. `explain` plans a `find` without running it and says `"executed": false`, so it is not evidence of what a count did. `find` and `aggregate` reject `diagnostics`.

`find` accepts `select`, `orderBy`, `limit`, and `cursor`, returning `records`, `hasMore`, and `nextCursor`. Repeat the same query with the returned cursor. Primary-key fields are appended to the sort as tie-breakers. `requireIndex` rejects plans that cannot narrow candidates through an index. `explain` reports adapter-specific plan details. Pebble handles primary/unique lookups, leading compound equality/range/prefix predicates, and compatible index ordering; complex OR/IN combinations may require bounded scans and sorting.

`update` accepts `set` and `increment`. `delete` accepts the same selection rules. Use an exact `key`, a `where` filter, or both; full-collection mutation requires `all:true`. With a `key`, `where` is a guard: the record changes only if it also matches, checked in the same write transaction. A failed guard reads like an absent record: `affected: 0`, or `version_conflict` with `ifVersion`, so it does not reveal whether the record exists. `maxAffected` defaults to 1. Exceeding the bound aborts all changes. `ifVersion` requires an exact key. Metadata and primary keys cannot be patched.

`upsert` uses the primary key or a named unique `conflictIndex`. It patches supplied fields and preserves the others. Conflict-index fields must be supplied and non-null. A unique index allows multiple null-containing tuples, consistently across all three adapters.

`batch` takes `operations:[{op,args}]` and executes record writes across collections in ONE database atomically. Supplying another database, schema operations, or nested batches is rejected. Later operations see earlier writes. Uniqueness is checked after each operation, so temporarily conflicting key swaps are rejected.

A batch of only reads (`get`, `find`, `count`, `aggregate`) runs in one read transaction, so every result comes from the same snapshot. It needs `database.read` (or a `read` app grant), accepts `timeoutMs` like a query, and returns `{results:[...]}` in operation order, limited to 4 MiB in total. Reads and writes cannot be mixed in one batch. On AptevaDB, up to four reads of a batch (at most one per CPU) run at a time on that snapshot, each with its own 16 MiB work budget; the first failure in operation order fails the batch, and no further read starts after a failure or once the results exceed 4 MiB. SQLite and Pebble run them one after another. Reads that share a collection, filter and grouping are faster as one aggregate with a `where` per metric: it reads each record once instead of once per read.

Pebble automatically groups concurrent record-write requests per database, including across collections, while keeping each request atomic. Every success waits for a synchronous disk flush. There is no artificial batching delay; a lone writer still pays the flush cost. Up to 64 requests may wait behind the active group; overflow returns `resource_limit`. Grouped batches flush after reaching 1 MiB of encoded writes, without splitting a single API transaction. Canceled requests are skipped before staging; staged writes wait for a definitive commit outcome.

## HTTP and app-to-app access

Admin endpoint: `POST /operations/<operation>` using the same JSON arguments, without the `db_` prefix. It requires the SDK bearer token and a trusted `X-Apteva-Project-ID` header. The platform's app proxy strips and rebuilds that header. Agents, sibling apps, and delegated-user callers cannot use this admin route to bypass MCP grants. Never expose a sidecar token to browser clients; dashboard requests go through the platform proxy.

MCP handlers require the SDK's authenticated caller/project context, reject project overrides, and enforce `database.read`, `database.write`, or `database.manage` grants. Resource names are `database/<name>`. Listing filters inaccessible databases. Delegated-user access is disabled in this version.

Consumers declare `platform.apps.call` and a `requires.apps` dependency on `database`, then call:

```go
err := ctx.PlatformAPI().CallAppResult("database", "db_find", map[string]any{
    "database": "default",
    "collection": "contacts",
    "limit": 50,
}, &result)
```

Each calling app gets its own private namespace within the authenticated project, so `"database": "prod"` from an app means that app's private `prod`, not the project's. To use a project-owned database, the app passes `"scope": "project"`, and a project operator must first grant that app access to that database:

```json
{"database": "prod", "app": "graphql", "access": "read"}
```

Grants are made with `db_access_grant` (agents need `database.manage` on the database) or `POST /operations/access_grant` from the dashboard surface, listed with `db_access_list` and removed with `db_access_revoke`. They are keyed by the caller's server-asserted app name, so they apply to that app's install in this project only. `read` allows describe, list, get, find, count, aggregate, explain, read-only batches, `db_query` and `db_changes`; `write` adds insert, update, delete, upsert and batch. Calling apps can never create, drop or alter project databases, collections or indexes, configure realtime capture, or change grants. Grants require an existing database, and dropping a database removes its grants. `db_databases_list` with `scope: "project"` lists only granted databases. Agent and grant-policy checks (`database.read`/`write`/`watch`) still apply on top. Administration of another app's private databases is future work.

## Initial limits and remaining work

Limits are currently fixed: 128 databases per scope and 128 open handles per process; 128 declared fields; 16 secondary indexes with up to 8 fields each; 1 MiB records; 8 MiB requests; 4 MiB find/aggregate result payloads; 1,000 records per write or page; 100 operations per batch. Integer values and counts use decimal strings to retain all 64 bits.

`find`, `count`, and `aggregate` default to a 30-second deadline on all three adapters. Set top-level `timeoutMs` to 1–60000 to override it; omitted or 0 uses the default. An earlier caller deadline takes precedence. Other endpoints retain their two-second deadline. The timeout is independent of the query/cursor, so it can change between pages.

Pebble has no fixed scanned-candidate limit or cumulative input-byte limit. It streams records, retains only the requested top page for non-indexed sorts, and bounds retained results and aggregation group state with a 16 MiB accounting budget. This is an estimate of retained values and overhead, not a cap on process RSS. Compatible cursor queries seek directly into their index. Queries can therefore scan collections much larger than 10,000 records or 16 MiB; excessively costly queries fail explicitly on deadline or retained-memory limits without returning partial counts/aggregates. `requireIndex` remains available to reject queries without a selective index. SQLite relies on native plans, result/work-buffer limits, and cancellation; it does not enforce a VM-instruction cap. See the [large-scan verification](benchmarks/LARGE-SCANS.md).

Pebble scans reuse a record iterator, materialize the fields needed for the query, and decode full documents after filtering when required for output. Aggregates use typed accumulators and format counts/integers when producing their output. These optimizations preserve the existing storage format and durability settings; see the [scan optimization measurements](benchmarks/OPTIMIZED-SCANS.md).

Index creation is synchronous and atomic on all three adapters. SQLite and Pebble are limited to 10,000 existing records and the query work buffer. AptevaDB counts the records and builds the index natively, up to 1,000,000 existing records within a 30 s deadline (0.5–0.9 s for 328,000 records of about 600 bytes); it checks uniqueness itself and leaves no index behind when a build fails. Builds block access to that database while its write lock is held. Create indexes before large imports. Background index jobs/rebuilds and read-through maintenance are later milestones.

The proposal also includes features that are **not implemented yet**: schema alteration/defaults, request-id deduplication, granular shared database bindings, backups and portable export/import, events/outbox, configurable quotas, handle eviction, comprehensive crash fault injection, and sustained capacity testing. The dashboard is an initial administration surface; record updates/deletes and index/database removal are available through the API. Cloud adapters and migrating CRM/Tables remain separate projects.

## Validation

```sh
GOTOOLCHAIN=local GOWORK=off go test ./...
GOTOOLCHAIN=local GOWORK=off go test -race ./...
GOTOOLCHAIN=local GOWORK=off go vet ./...
```

Tests cover all three adapters, aggregation/null behavior, generic indexes, concurrent uniqueness, composite/int64 keys, differential queries, cursor pagination, batch rollback, persistent reopening, project/app isolation, and a compiled sidecar exercised over HTTP and MCP. Tests use temporary data directories; no existing app data is used.

## Performance

See [benchmark results](benchmarks/RESULTS.md) for local SQLite/Pebble measurements, observed timeout failures, and current scan/index limits. The [benchmark guide](benchmarks/README.md) documents the reproducible runner. These engine-contract measurements include result encoding but exclude HTTP/MCP transport and UI latency.

[Durable Pebble write batching](benchmarks/GROUPED-WRITES.md) improved the controlled 8-writer workload by 4× and the 32-writer workload by 12.4× while retaining synchronous commits.
