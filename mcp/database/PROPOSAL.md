# Database app proposal

Status: v0.1.0 implemented. Date: 2026-09-07. See README.md for the implemented subset and remaining milestones.

## 1. Decision and scope

Build `database` as an independent Apteva sidecar app in `apps/mcp/database`, using app-sdk, its own manifest, release version, persistent volume, MCP tools, HTTP API, and dashboard panel.

Provide one versioned record contract over two local adapters: SQLite by default and Pebble as an alternative. Both must implement the same initial collection, record, query, index, and atomic-write semantics. Apps select a database binding, then use the same operations regardless of its adapter.

Database can be installed and used directly by agents and people. CRM, Tables, and other apps can become clients later; their migration is not part of this proposal. Database does not replace app-sdk's internal app database facility. Existing apps do not change their persistence automatically.

The first release is entirely local: files in the app's persistent volume on the host running Apteva, including when that host is a server. No cloud connections, integration changes, replication, or synchronization. The adapter boundary leaves room for future providers without claiming every future provider will satisfy the full contract.

## 2. Product model

Hierarchy: authorized project → named database → collection → records and indexes.

- A project gets a SQLite database named `default` on first use.
- Additional databases have explicit names and an immutable adapter choice.
- A collection has a schema, primary key, secondary indexes, and schema version.
- A record is a JSON object validated against that schema.
- An app binding resolves a logical database name to an authorized database ID.
- A globally installed Database app still isolates data by project. App-private databases are the default for app-created storage; sharing requires an explicit grant.

Example: `default` uses SQLite; `events` uses Pebble. Both can hold a `contacts` collection and answer the same `find` request. They are independent datasets. Changing an adapter means exporting and importing into another database, followed by an explicit binding change.

## 3. Architecture

```text
Agents / dashboard / consuming apps
                 |
          MCP and HTTP endpoints
                 |
     authorization and database resolution
                 |
   common validation, types, query AST, limits
                 |
            adapter interface
           /                 \
     SQLite adapter       Pebble adapter
     SQL + indexes        records + ordered indexes
           |                 |
       local file        local directory
```

Common code defines behavior and validates inputs. Each adapter plans and executes whole operations close to storage. Shared code must not turn an indexed query into a loop of remote or per-record calls.

Use app-sdk for lifecycle, platform calls, tools, UI, and authorization integration. Never import server internals. Consumers declare the Database app dependency and call it through `ctx.PlatformAPI().CallAppResult(...)`. A later SDK helper can make calls ergonomic; it remains a client, not an embedded database engine.

At implementation time, verify which authenticated caller/project/grant fields the SDK exposes and add any necessary SDK support. A user-supplied `project_id` is not sufficient authorization.

## 4. One public contract

Version the JSON contract as `database/v1`. MCP tools use a `db_` prefix; HTTP and any client helper share the same request and result definitions.

| Operation | Behavior |
|---|---|
| `get` | Fetch a record by its primary key; return `found` and `record`. |
| `find` | Filter, project fields, order, and paginate records. |
| `insert` | Insert a bounded array of records atomically. |
| `update` | Patch records selected by a key or filter atomically. |
| `delete` | Delete records selected by a key or filter atomically. |
| `upsert` | Insert or patch using the primary key or a named ready unique index. |
| `count` | Return an exact count or a resource-limit error. |
| `aggregate` | Filter records, group by scalar fields, and compute count, sum, average, minimum, and maximum. |
| `batch` | Execute a bounded list of writes atomically within one database. |
| `explain` | Describe access path, index choice, residual filters, and sorting without executing the query. |

Management operations cover database create/list/describe/drop, collection create/list/describe/alter/drop, index create/list/describe/drop/rebuild, job status/cancel, and local backup/restore plus portable export/import.

Destructive management operations require an explicit confirmation field and matching scope authorization. Bulk record mutations require a filter or `all: true`, plus `maxAffected`. Exceeding that bound rolls back the entire operation; it does not modify an arbitrary subset. Default `maxAffected` is 1.

`update` supports `set` and numeric `increment`; they cannot target the same field. Unspecified fields remain unchanged. Setting null is explicit. No arbitrary expressions or executable code. `upsert` patches supplied fields on conflict and preserves unspecified fields; its conflict fields must be non-null, and record primary keys cannot change.

`batch` is a single request with a predefined list of writes. It can span collections in one database, but not databases, app calls, or schema operations. Caller-assigned IDs allow records in the batch to reference each other without a transaction session API.

## 5. Record and schema semantics

Require explicit schemas so both adapters can validate data, encode indexes correctly, and avoid ambiguous comparisons. Undeclared fields are rejected; a declared `json` field holds flexible nested content.

Initial field types:

| Type | Contract |
|---|---|
| `text` | Valid UTF-8, case-sensitive byte ordering; no implicit Unicode normalization. |
| `integer` | Signed 64-bit; decimal strings on the wire preserve exact values. |
| `number` | Finite IEEE-754 double; normalize negative zero. |
| `boolean` | True or false. |
| `datetime` | RFC3339 input normalized to UTC microsecond precision. |
| `json` | Nested JSON storage and retrieval; no generic nested filtering or indexing in v1. |

No implicit conversion between text and numbers. Integer increments detect overflow. Datetime inputs with non-zero sub-microsecond precision are rejected rather than silently truncated.

Primary keys may be one or several non-null scalar fields, with explicit key objects in `get`. Composite keys and index fields have bounded encoded sizes. When omitted from a collection definition, the default primary key is an app-generated UUID string field named `id`. There is no adapter-specific auto-increment behavior in the portable contract.

Reserve `_created_at`, `_updated_at`, and `_version`. The service owns them; successful updates increment the version. A key-targeted mutation can supply `ifVersion` for optimistic concurrency. Timestamps do not determine ordering between commits; versions do.

Omitted fields on insertion receive their declared literal default, otherwise null if nullable, otherwise fail validation. Stored schema fields are always present. `is_null` and `is_not_null` are explicit predicates; comparisons against null are rejected. Ascending sorts place null first and descending sorts place it last.

Initial schema evolution supports creating/dropping collections and adding nullable fields without a non-null default. Reads materialize null for older records; adapters must preserve this behavior for queries and newly built indexes. Renames, field drops, type changes, and backfills are later migration jobs. Every schema or index-definition change increments the schema version.

## 6. Query format and pagination

```json
{
  "database": "default",
  "collection": "contacts",
  "where": {
    "and": [
      {"field": "status", "op": "eq", "value": "active"},
      {"field": "created_at", "op": "gte", "value": "2026-01-01T00:00:00Z"}
    ]
  },
  "select": ["id", "name", "email"],
  "orderBy": [{"field": "created_at", "direction": "desc"}],
  "limit": 50
}
```

Initial operators: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in`, `is_null`, `is_not_null`, and text `starts_with`, composed with explicit `and`/`or`. Bound AST depth, term count, and `in` values. Predicates are type checked before planning. String prefix matching uses literal bytes, not wildcard patterns or locale-dependent comparison.

Return `records`, an opaque `nextCursor`, and `hasMore`; do not run an automatic total count. Order defaults to primary-key ascending. Explicit ordering appends any missing primary-key fields as a deterministic tie-breaker. Cursors carry the complete ordering values even when projection excludes them.

Use keyset pagination, not growing offsets. Cursors are authenticated and bound to the database, authorized scope, collection, schema version, and normalized query; they have an expiry. Each page has a consistent read snapshot. Separate page requests observe fresh snapshots: concurrent updates to sort keys can cause records to move across pages. Stable multi-page exports use a separate snapshot export job.

## 7. Generic index contract

Aggregation is part of the common contract on both local adapters. For example:

```json
{
  "database": "sales",
  "collection": "orders",
  "where": {"field": "status", "op": "eq", "value": "paid"},
  "groupBy": ["country"],
  "metrics": [
    {"name": "orders", "op": "count"},
    {"name": "revenue", "op": "sum", "field": "amount"},
    {"name": "average_order", "op": "avg", "field": "amount"}
  ],
  "orderBy": [{"field": "revenue", "direction": "desc"}],
  "limit": 50
}
```

`count` without a field counts records; with a field it counts non-null values. Other metrics ignore nulls. Empty ungrouped input produces one row with count zero and other metrics null; empty grouped input produces no rows. Null grouping keys form a group. Integer sums and counts return exact decimal strings; integer overflow fails. Averages return floating-point numbers, and floating-point aggregates are approximate. `sum`/`avg` accept numeric fields, while `min`/`max` accept ordered scalar fields. Ordering uses output group names and metric aliases, with grouping fields as deterministic tie-breakers. The output limit applies after aggregation; never aggregate only the first page of input. A bounded output can report truncation, but an input work-limit failure must return an error. SQLite uses native GROUP BY and aggregate functions; Pebble reduces filtered index scans with bounded memory. Grouping and filtering may benefit from indexes; arbitrary aggregation is not guaranteed constant-time.

Indexes are first-class objects with backend-independent definitions:

```json
{
  "database": "default",
  "collection": "contacts",
  "name": "by_status_created",
  "fields": [
    {"field": "status", "direction": "asc"},
    {"field": "created_at", "direction": "desc"}
  ],
  "unique": false
}
```

The initial contract supports single-field, compound, unique, and mixed ascending/descending ordered indexes on declared scalar fields. The primary key has an automatic unique index. Secondary indexes internally include the primary key as a deterministic tie-breaker, but uniqueness applies only to the user-declared index fields.

Index rules:

- Field order matters. Leading equality fields followed by a range or ordering field are the principal efficient pattern.
- Nulls are indexed for filtering and sorting. In a unique index, records with null in any declared index field do not conflict with each other.
- Uniqueness is checked against the committed state and earlier writes in the same batch. Temporary uniqueness violations fail even if a later batch operation would remove them.
- Duplicate existing values cause unique-index creation to fail; no incomplete index becomes usable.
- Repeated create with the same name and identical definition returns the existing index; a different definition returns `schema_conflict`.
- Primary-key indexes cannot be dropped. Queries never use indexes that are building, failed, or being dropped.
- No automatic index creation during reads. `explain` may suggest an index; an explicit operation creates it.

Expression, partial, full-text, vector, array, JSON-path, and user-configured covering indexes are outside v1. Generic support means both engines implement these ordered-index semantics, not that every kind of index is initially supported.

For example, a unique index on `email` supports conflict-safe contact upsert. `(status ASC, created_at DESC)` supports active contacts ordered newest first. A query on `created_at` alone is not promised an efficient access path through that compound index.

## 8. Query execution and cost controls

Planning should recognize primary-key lookup, unique lookup, compound equality prefixes, bounded ranges, prefix scans, index-provided ordering, and residual predicates. Pebble v1 can choose one index per AND branch; index intersection is optional later. OR branches may use multiple scans with deduplication and a bounded merge; otherwise they fall back to a bounded scan.

SQLite compiles validated queries to parameterized SQL and uses native query planning. Pebble compiles the same AST into ordered key ranges, residual evaluation, and bounded sorting when needed. Avoid building an elaborate shared cost optimizer that duplicates SQLite; share semantics and plan reporting, not an identical physical plan.

`explain` reports the selected index, lookup/range/full-scan classification, residual predicates, whether sorting is required, and estimates only where available. Estimates are labeled; they are not latency promises. A query can request `requireIndex: true`, which requires an index that narrows candidate selection, rather than merely scanning an entire index.

Proposed configurable starting limits:

- 100 records per page by default; 1,000 maximum.
- 1 MiB per record, 4 MiB per query response, and 8 MiB per write request.
- 1,000 affected records per atomic write request or batch.
- No fixed candidate-count ceiling. Read queries (`find`, `count`, `aggregate`) default to 30 seconds and accept `timeoutMs` up to 60 seconds; earlier caller deadlines win. Other endpoints retain a two-second deadline.
- A 16 MiB accounting budget for retained Pebble query results and aggregation group state. Streaming scans may visit more data than this budget; non-indexed find sorts retain only the requested top page.
- At most eight fields per index and sixteen secondary indexes per collection.

Enforcement is adapter-specific: Pebble checks cancellation while streaming and accounts for retained work; SQLite combines native planning, result/work-buffer limits, and cancellation. Do not claim a strict SQLite VM-instruction cap or that a row output limit bounds scan cost. Synchronous index construction remains a separate operation limited to 10,000 existing records; create indexes before large imports until background index jobs are available.

Queries exceeding a work or time limit fail explicitly. A successful page may end at its row or byte budget with a valid continuation cursor; this does not signify a complete result set. Count never returns a partial value labeled exact. Mutation timeouts roll back unless the commit already succeeded, in which case idempotency resolves the uncertain response.

## 9. SQLite adapter

Use one SQLite file per logical database, with generated physical table/index names and adapter-owned metadata. Compile declared scalar fields into typed physical columns with explicit validation. Keep JSON as encoded JSON values. Ensure primary-key fields are explicitly non-null and typed/collated to match the portable contract.

Use native compound/unique indexes, parameterized queries, WAL mode, a busy timeout, and a deliberately configured durable commit policy. Translate text-prefix bounds and null ordering explicitly where necessary. Row mutations, secondary index maintenance, versions, and idempotency receipts commit in the same transaction.

SQLite's existing query planner supports multi-column indexes and can use them for searching and ordering. Its unique indexes allow multiple null values, matching the proposed contract. These are backend mechanisms; the proposed cross-adapter behavior remains governed by contract tests. Sources: [SQLite query planning](https://www.sqlite.org/queryplanner.html), [CREATE INDEX](https://www.sqlite.org/lang_createindex.html).

Tables is useful reference code for validation, typed columns, SQL generation, and limits, but the new app must not share or take ownership of Tables' existing database files.

## 10. Pebble adapter

Pebble is a key/value engine with ordered iteration, batches, and snapshots. It does not provide a relational query layer or general transactions. Our adapter must implement the record and index layer. Source: [Pebble documentation](https://github.com/cockroachdb/pebble).

Store, schematically:

```text
schema/<collection-id>                       collection definitions
record/<collection-id>/<encoded-primary-key> record values
index/<collection-id>/<index-id>/<tuple>/<pk> secondary entries
unique/<collection-id>/<index-id>/<tuple>     uniqueness ownership
receipt/<caller-scope>/<request-id>           idempotency receipt
```

Actual keys use a versioned binary encoding with length-safe boundaries and type tags, not concatenated user strings. Order-preserving encoders handle signed integers, finite doubles, UTF-8 text, booleans, UTC microseconds, null ordering, composite tuples, and per-field descending order. Null-containing tuples do not receive uniqueness ownership keys.

Serialize writes per database in v1. Hold the write mutex from match selection and constraint checks through durable batch commit. Use an indexed batch so later operations see earlier writes. Atomically update the record, remove stale index entries, add new entries, update unique ownership, increment versions, and store the request receipt.

Use snapshots for read requests. Only this process opens and mutates these files. This deliberately implements bounded atomic service operations; it does not imply Pebble supplies transaction isolation by itself. Per-database serialization is a throughput tradeoff to measure before considering finer-grained locking.

## 11. Index lifecycle and recovery

Index management exposes a job with `building`, `ready`, `failed`, and `dropping` states and progress where available.

For v1, index creation and rebuilds pause writes to the affected logical database, with reads continuing against the old ready schema. This makes uniqueness validation and rebuild correctness tractable. Maintenance has a configured maximum duration; new writes receive a retryable maintenance response rather than waiting indefinitely.

Build under a new index generation. Validate all existing records; publish the ready definition atomically only after successful completion. A failed rebuild preserves the old ready generation. On restart, incomplete generations are ignored and cleaned up or rebuilt. Dropping first removes the index from the planner, then reclaims its physical storage after readers finish.

SQLite uses native transactional DDL; Pebble builds a private key prefix and atomically switches its metadata. Keep authoritative schema/index state inside the same adapter store as the records, avoiding a cross-engine transaction with the app's registry database. Zero-downtime concurrent index builds are a later feature.

## 12. Reliability, authorization, and storage

The SDK-managed app database stores the registry, grants, and administrative jobs. Each adapter owns authoritative collection metadata, indexes, records, and write receipts. Database creation uses a recoverable creating→ready state and an on-disk identity marker; startup reconciles interrupted registry/file operations.

The service derives project and caller identity from verified platform context, checks grants before opening a database, and scopes every operation. Cross-project data access is absent from v1. Distinguish read, write, schema-management, and database-administration grants. Never accept arbitrary filesystem paths through tools or browser requests.

Suggested volume layout:

```text
/data/control.db
/data/databases/<generated-db-id>/data.sqlite
/data/databases/<generated-db-id>/pebble/
/data/backups/<generated-backup-id>/
```

Database IDs are globally unique within the install and registry entries carry ownership. Configure disk quotas and monitor free space, index size, maintenance state, and write failures. On unmount, drain requests and close all handles. Ordinary process restart retains data.

Writes may provide `requestId`. Store its scope, normalized request hash, and compact result in the same commit as the data. Repeating it returns the saved result; a different request with the same ID fails. Publish a finite retention window, proposed at 24 hours, and its storage quota. Receipts cannot guarantee deduplication after expiry. Default responses return keys and counts; returned full records are optional and byte-limited.

Standard errors include `not_found`, `invalid_argument`, `schema_conflict`, `unique_conflict`, `version_conflict`, `permission_denied`, `query_too_expensive`, `resource_limit`, `maintenance`, and `storage_error`. Include retryability and structured details without exposing physical paths or raw backend internals.

## 13. Backup, portability, and events

Provide adapter-native local snapshot backups and a portable export format containing contract version, schema, primary keys, index definitions, and streamed records with checksums. Never copy a live SQLite file or Pebble directory naively. Use supported SQLite backup/snapshot facilities and Pebble checkpoints, with the service coordinating consistency.

A backup captures one logical database and its manifest. Import/restore targets a new database ID, validates data, rebuilds indexes, and becomes visible only after completion. Access grants must be explicitly established for the restored database. Existing databases are never overwritten implicitly.

Portable export/import provides the later path between SQLite and Pebble. It preserves record keys and versions; it does not translate physical files. Schema changes are paused for export; long-running snapshots have disk/time limits. Define separate restore semantics for idempotency receipts so a clone does not replay another database's requests.

If mutation events are shipped, use an outbox committed alongside each write and publish after commit with stable event IDs. Delivery is at least once and consumers deduplicate. Include scope, collection, operation, and bounded key information; avoid broadcasting full record contents by default. Events are optional for the first implementation milestone.

## 14. Dashboard

Ship an independent Database panel with:

- Database list, adapter selection at creation, disk usage, and health.
- Collection creation, schema inspection, and record browsing/editing.
- A query builder using the same portable filter contract.
- Index management showing fields, uniqueness, build state, and maintenance progress.
- Query explanation showing index use, scan behavior, and any sorting.
- Backup/export/import jobs and clear results for conflicts or failed operations.

This is the Database app's own administrative interface. Tables keeps its current product and storage until a separately scoped adoption project.

## 15. Suggested implementation layout

```text
mcp/database/
  apteva.yaml
  main.go
  go.mod
  api/                 request/result schemas and error definitions
  service/             authorization, limits, registry, and dispatch
  query/               typed AST, normalization, cursor handling
  adapters/
    adapter.go         backend interface
    sqlite/            SQL compiler and native storage
    pebble/            key encoding, record/index store, planner
  conformance/         adapter-shared behavior and recovery tests
  migrations/          control database migrations
  ui/                  dashboard panel and icon
  skills/              agent usage guidance
  scenarios/           end-to-end app scenarios
```

The adapter interface includes collection/index lifecycle, record operations, batch, explain, snapshot export, health, and close. Typed Go interfaces mirror the public contract; private adapter methods may differ. Adapters live inside the independent Database app initially, not in separate sidecars. Pin app-sdk to the latest tag by commit topology when implementation starts, following workspace guidance.

## 16. Delivery and acceptance

1. **Freeze the contract:** JSON schemas, type/null/order rules, query grammar, index definitions, errors, and a shared conformance suite.
2. **Build the independent app with SQLite:** lifecycle, authorization, collection and record operations, generic indexes, atomic batches, cursors, and query explanation.
3. **Implement Pebble parity:** ordered encoding, records, secondary/unique indexes, planning, atomic writes, durable receipts, and recovery.
4. **Complete operations and UI:** index jobs, dashboard, backups, portable export/import, limits, and health reporting.
5. **Qualify both adapters:** randomized differential tests, concurrency tests, crash/restart tests, representative benchmarks, and app-to-app scenarios.

Acceptance requires identical observable results across adapters for types, defaults, nulls, unique constraints, updates, upserts, batch rollback, ordering, and cursor pagination on static datasets. Tests must verify cross-project and app-private isolation, concurrent uniqueness conflicts, optimistic version conflicts, interrupted index builds, failed rebuild preservation, restart durability, retry receipts, and backup restoration.

Benchmark primary-key reads, unique lookup, compound range/order queries, unindexed filtering, bulk insert, updates affecting multiple indexes, index builds, and paginated reads at 10k, 100k, and 1m records. Capture candidate work, latency distributions, memory, disk size, and write amplification where measurable. Check that indexed lookups do not scan the full collection. Establish throughput and latency targets on the actual deployment hardware; do not assume Pebble will beat SQLite for this record workload.

The release is complete when both local adapters pass the contract suite and the app works independently. Cloud integrations, CRM/Tables migrations, joins, raw SQL, advanced aggregation pipelines, replication, and online index rebuilding remain separately scoped follow-up work.
