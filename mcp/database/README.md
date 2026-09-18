# Database

Independent Apteva sidecar app, version 0.4.2. SQLite and Pebble implement one local record API. The app owns its files and does not change CRM, Tables, or other apps' databases.

## Working in this version

- Several named databases per authenticated project or calling-app scope, with SQLite as the default and an explicit adapter for each database.
- SQLite collections use a typed, `WITHOUT ROWID` storage layout with native composite primary keys. Existing v1 collections remain readable and migrate atomically on their first write through a shadow-table swap.
- SQLite uses a serialized writer with persistent prepared statements, a separate four-connection read-only pool, and checkpoint work amortized through a larger WAL threshold. The `durable` profile is the default; the opt-in `balanced` profile uses `synchronous=NORMAL` with a wider crash-loss window.
- SQLite bulk inserts use compiled multi-row statements inside the same atomic transaction, reducing per-record statement overhead without weakening durable commits.
- Several independently defined collections in every database.
- Typed records, UUID defaults, composite primary keys, exact int64 values encoded as decimal strings, and service-owned timestamps/versions.
- `get`, `find`, `insert`, `update`, `delete`, `upsert`, `count`, `aggregate`, `batch`, and `explain`.
- Generic single-field/compound, ascending/descending, and unique indexes. Native SQLite indexes; ordered secondary and uniqueness keys in Pebble.
- Atomic record writes and batches across collections in one database; optimistic version checks. Pebble automatically groups concurrent record writes into shared durable commits.
- Filtering, projection, stable ordering, and signed cursor pagination. A cursor expires after one hour and becomes invalid after an index/schema change. Pages observe fresh snapshots; changing sort keys between pages can move records.
- MCP tools, an authenticated admin HTTP API, and a dashboard for database/collection creation, browsing, inserts, queries, aggregation, and indexes.
- Project isolation, grant checks on MCP, and separate private namespaces for authenticated calling apps.

Both adapters are embedded and local to the Apteva host. Choosing Pebble does not change the record or aggregate request format.

## Build and run

Use Go 1.25.1 or newer. This module pins app-sdk v0.81.0, the latest tag at the SDK checkout's HEAD for this release.

```sh
cd apps/mcp/database
GOWORK=off go build -o /tmp/apteva-database .
APTEVA_DATA_DIR=/tmp/apteva-database-data \
DB_PATH=/tmp/apteva-database-data/sdk.db \
APTEVA_APP_TOKEN=local-development-token \
APTEVA_APP_PORT=8098 \
/tmp/apteva-database
```

In this workspace, use `GOTOOLCHAIN=local GOWORK=off` to use the installed Go 1.25.1 toolchain; the root workspace currently requests a different, unavailable toolchain. The new module is also listed in the root `go.work` for normal workspace development once that toolchain is available.

The SDK starts `/health` and `/mcp`, authenticates app routes, and serves `ui/`. The manager stores its registry and managed databases below `ctx.DataDir()/database/`. Do not share this directory between multiple running app processes.

Build the dashboard panel from the apps repo:

```sh
bun run scripts/build-panels.ts --app database
```

## Example: multiple databases and collections

Call `db_database_create` twice:

```json
{"database":"sales","adapter":"sqlite"}
```

```json
{"database":"archive","adapter":"pebble"}
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

`count` with no field counts records. `count` with a field counts non-null values; other metrics skip null. Empty ungrouped input yields one row with zero counts and null other metrics. Empty grouped input yields no rows. Integer sums/counts are decimal strings and integer overflow fails; averages and floating sums are approximate. `min`/`max` support scalar fields, while `sum`/`avg` require numeric fields. No cross-collection joins, date buckets, distinct aggregates, or custom expressions yet.

## Query and mutation rules

Filters use `{field,op,value}` leaves and explicit `{and:[...]}` / `{or:[...]}` groups. Operators: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in`, `is_null`, `is_not_null`, `starts_with`. Null predicates omit `value`. String matching is literal, case-sensitive UTF-8 byte comparison.

`find` accepts `select`, `orderBy`, `limit`, and `cursor`, returning `records`, `hasMore`, and `nextCursor`. Repeat the same query with the returned cursor. Primary-key fields are appended to the sort as tie-breakers. `requireIndex` rejects plans that cannot narrow candidates through an index. `explain` reports adapter-specific plan details. Pebble handles primary/unique lookups, leading compound equality/range/prefix predicates, and compatible index ordering; complex OR/IN combinations may require bounded scans and sorting.

`update` accepts `set` and `increment`. `delete` accepts the same selection rules. Use an exact `key` or a `where` filter; full-collection mutation requires `all:true`. `maxAffected` defaults to 1. Exceeding the bound aborts all changes. `ifVersion` requires an exact key. Metadata and primary keys cannot be patched.

`upsert` uses the primary key or a named unique `conflictIndex`. It patches supplied fields and preserves the others. Conflict-index fields must be supplied and non-null. A unique index allows multiple null-containing tuples, consistently across both adapters.

`batch` takes `operations:[{op,args}]` and executes record writes across collections in ONE database atomically. Supplying another database, schema operations, or nested batches is rejected. Later operations see earlier writes. Uniqueness is checked after each operation, so temporarily conflicting key swaps are rejected.

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

Each calling app install gets its own private namespace within the authenticated project. The dashboard currently operates on project-owned databases; shared bindings and administration of another app's private databases are future work.

## Initial limits and remaining work

Limits are currently fixed: 128 databases per scope and 128 open handles per process; 128 declared fields; 16 secondary indexes with up to 8 fields each; 1 MiB records; 8 MiB requests; 4 MiB find/aggregate result payloads; 1,000 records per write or page; 100 operations per batch. Integer values and counts use decimal strings to retain all 64 bits.

`find`, `count`, and `aggregate` default to a 30-second deadline on both adapters. Set top-level `timeoutMs` to 1–60000 to override it; omitted or 0 uses the default. An earlier caller deadline takes precedence. Other endpoints retain their two-second deadline. The timeout is independent of the query/cursor, so it can change between pages.

Pebble has no fixed scanned-candidate limit or cumulative input-byte limit. It streams records, retains only the requested top page for non-indexed sorts, and bounds retained results and aggregation group state with a 16 MiB accounting budget. This is an estimate of retained values and overhead, not a cap on process RSS. Compatible cursor queries seek directly into their index. Queries can therefore scan collections much larger than 10,000 records or 16 MiB; excessively costly queries fail explicitly on deadline or retained-memory limits without returning partial counts/aggregates. `requireIndex` remains available to reject queries without a selective index. SQLite relies on native plans, result/work-buffer limits, and cancellation; it does not enforce a VM-instruction cap. See the [large-scan verification](benchmarks/LARGE-SCANS.md).

Pebble scans reuse a record iterator, materialize the fields needed for the query, and decode full documents after filtering when required for output. Aggregates use typed accumulators and format counts/integers when producing their output. These optimizations preserve the existing storage format and durability settings; see the [scan optimization measurements](benchmarks/OPTIMIZED-SCANS.md).

Index creation is synchronous and atomic on both adapters, limited to 10,000 existing records and the query work buffer. Builds block access to that database while its write lock is held. Create indexes before large imports. Background index jobs/rebuilds and read-through maintenance are later milestones.

The proposal also includes features that are **not implemented yet**: schema alteration/defaults, request-id deduplication, granular shared database bindings, backups and portable export/import, events/outbox, configurable quotas, handle eviction, comprehensive crash fault injection, and sustained capacity testing. The dashboard is an initial administration surface; record updates/deletes and index/database removal are available through the API. Cloud adapters and migrating CRM/Tables remain separate projects.

## Validation

```sh
GOTOOLCHAIN=local GOWORK=off go test ./...
GOTOOLCHAIN=local GOWORK=off go test -race ./...
GOTOOLCHAIN=local GOWORK=off go vet ./...
```

Tests cover both adapters, aggregation/null behavior, generic indexes, concurrent uniqueness, composite/int64 keys, differential queries, cursor pagination, batch rollback, persistent reopening, project/app isolation, and a compiled sidecar exercised over HTTP and MCP. Tests use temporary data directories; no existing app data is used.

## Performance

See [benchmark results](benchmarks/RESULTS.md) for local SQLite/Pebble measurements, observed timeout failures, and current scan/index limits. The [benchmark guide](benchmarks/README.md) documents the reproducible runner. These engine-contract measurements include result encoding but exclude HTTP/MCP transport and UI latency.

[Durable Pebble write batching](benchmarks/GROUPED-WRITES.md) improved the controlled 8-writer workload by 4× and the 32-writer workload by 12.4× while retaining synchronous commits.
