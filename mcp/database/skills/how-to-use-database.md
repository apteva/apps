# Database app

Use this app for local structured data shared through a portable record contract. First discover databases with `db_databases_list`, then collections with `db_collections_list` and their schema with `db_collection_describe`. Data calls select a `database` (default: `default`) and stored `collection`, or an optional named pipeline for `find`/`aggregate`. The canonical record operations are `get`, `find`, `insert`, `update`, `upsert`, `delete`, `count`, `aggregate` and `batch`. Several databases and collections are supported. SQLite, Pebble and AptevaDB use the same arguments.

Create the database and explicit collection schema before writing. Undeclared fields are rejected. Integers are decimal strings, booleans are JSON booleans, and datetimes are RFC3339 with at most microsecond precision. Nullable fields may be omitted or null. Primary keys default to generated UUID `id` values; read the schema for custom/composite keys.

Use `db_find` with typed `{field,op,value}` predicates, optional `and`/`or`, `select`, `orderBy`, and `limit`. Follow `nextCursor` with the same query. Use `db_get` with the exact primary-key object when known. Null comparisons use `is_null`/`is_not_null`, without a value. For a literal substring in one text field, where `%` and `_` must not act as wildcards, use `{field:"label",op:"contains_ascii",value:"100%"}`. For a search box over several text fields use one node `{fields:[...],op:"like_contains_ascii",value:"..."}`: it matches the fields joined by a space as `LIKE "%value%"` (ASCII case-insensitive, `%`/`_` wildcards, empty value matches all). It cannot use an index, so AND it with the indexed scope (for example tenant and date range) rather than searching the whole collection. `not_in` is the negative of `in`; a null value does not satisfy it. To filter by related records without fetching them, use `{exists:{collection,on:[{localField,foreignField,comparison?}],where?}}` or `{not_exists:{...}}` in `where` of `find`, `count` or `aggregate`: every pair in `on` must match (AND, as in SQL, so composite keys work); when a record can relate in several ways use `anyOf:[{on:[...]},{on:[...]}]` instead of `on` (OR). A `where` can hold further `exists`/`not_exists` nodes, up to 4 levels and 24 nodes per filter; a nested node reads `localField` from its immediate parent, and any failure inside one fails the whole operation. Nulls never match, `comparison:"text"` compares text with integer by exact decimal text, and each `on` list needs one indexed `foreignField` or the request fails with `query_too_expensive`. They are not available in metric `where`, `update` or `delete`. To see what a slow count did, pass `diagnostics:true` to `count`; `explain` only plans a find and runs nothing. Do not assume field names or invent IDs. `find` also accepts the same `lookups` to nest the referenced record under its `as` name (null when absent); filters and ordering stay on the collection's own fields, and `select` must include each `localField`.

Use `db_index_create` with `index:{name,fields:[{field,direction}],unique}` for repeated equality/range/sort queries. Compound field order matters. Put equality predicates first. Aggregates and dashboard batches use the same indexes: a dashboard filtered by scope and day should have a `(scope, day)` index, which also serves a list of scopes (`in`). `db_explain` shows the execution path. Indexes are created explicitly; no indexes are created automatically while reading. A failed unique-index build leaves existing data unchanged.

Use `db_aggregate` for summaries rather than fetching one page and computing totals. Arguments: `where`, `groupBy` (field names), `metrics:[{name,op,field?}]`, `orderBy`, `limit`. Operations: `count`, `sum`, `avg`, `min`, `max`. `count` without a field counts records; with a field it counts non-null values. Metrics ignore nulls. Integer sums/counts are decimal strings. Limits apply to output groups; work-limit errors never mean partial totals are exact. No grouping produces one summary row.

Dashboards usually need more than plain sums; keep them in one `db_aggregate` instead of fetching records:
- `count_distinct` counts distinct non-null values: `{name:"callers",op:"count_distinct",field:"caller_id"}`.
- Any metric takes its own `where`, so conditional metrics share one pass: `{name:"sales",op:"count",where:{field:"status",op:"eq",value:"sale"}}`, `{name:"revenue",op:"sum",field:"amount",where:{...}}`.
- `lookups:[{as:"campaign",collection:"campaigns",localField:"campaign_id",select?:["name"]}]` joins the ONE record whose single-field primary key equals `localField` (many-to-one). Use `"campaign.name"` in `where`, `groupBy` and metrics. It never duplicates the rows being counted; a null or dangling reference gives null lookup fields. Lookups can chain (`localField:"offer.campaign_id"` after an `offer` lookup). Join through the referenced collection's primary key; for one-to-many questions aggregate the many side instead.
- `buckets:[{name:"day",field:"created_at",unit:"day",timezone:"Europe/Paris"}]` derives a calendar key you can `groupBy`, `orderBy` or `count_distinct`. Units: `hour`, `day`, `week` (`weekStart` monday default or sunday), `month`, `year`. Calendar units return the local start date (`"2026-03-23"`); `hour` returns the UTC instant the local hour starts. Always pass the business timezone; the default is UTC.

Use `db_insert` with a records array; `db_upsert` uses primary keys or a named unique `conflictIndex`. `db_update` uses `set`/`increment` and `key` or `where`. Use `ifVersion` with a key to protect a read-modify-write operation. A `where` given with a `key` is a guard (for example the caller's scope): the record changes only if it matches, otherwise the result is `affected: 0`, or `version_conflict` with `ifVersion`, as for an absent record. Bulk updates/deletes need an explicit `maxAffected` bound (default 1); full-collection mutations require `all:true`. Deleting databases, collections, or indexes requires `confirm:true` and appropriate user intent.

`db_batch` atomically executes record writes across collections in the same database using `operations:[{op,args}]`. It cannot span databases or run schema operations. Do not automatically retry writes after an uncertain network response: request-id deduplication is not implemented yet. Resolve state first with a known key or unique index. A batch of only reads (`get`, `find`, `count`, `aggregate`) instead answers every operation from one consistent snapshot and needs only read access; use it when several dashboard numbers must agree with each other. Reads and writes cannot mix in one batch. `timeoutMs` applies to read batches. Several aggregates over the same collection, filter and `groupBy` should be one aggregate with a `where` on each metric (for example answered and missed calls as two filtered `count` metrics): it reads the records once. Never derive a global distinct count by summing per-group distinct counts; request it as its own `count_distinct` without `groupBy`.

Calling apps have private database namespaces; project-owned databases are separate. When the user wants an app (for example GraphQL) to use a project database, grant it explicitly with `db_access_grant` (`database`, `app` name, `access: "read"|"write"`); the app then passes `scope:"project"`. Grant only what the user asked for, prefer read, and remove it with `db_access_revoke`; `db_access_list` shows current grants. Apps never manage project schema or realtime capture. Cross-project access, cloud adapters, raw SQL, schema alteration and backups are not available in this version. Large Pebble scans and synchronous index builds have explicit work limits (AptevaDB builds indexes on up to 1,000,000 existing records; SQLite and Pebble on 10,000); prefer indexed queries and create indexes before large imports.

Choose `adapter: "aptevadb"` when creating a new database to use experimental embedded AptevaDB v0.2.0-alpha.18. SQLite remains the default; Pebble is also supported. All share the same typed records, indexes, aggregates and atomic batches. An existing database’s adapter cannot be changed in place.

AptevaDB writes stay synchronous. The app's `aptevadb_sync_mode` configuration (or `DATABASE_APTEVADB_SYNC_MODE`) accepts `full` (default), `barrier`, or `os`. On macOS, OS uses ordinary fsync like SQLite's default; OS/barrier provide weaker power-loss protection. Restart/remount after changing it. Read the active mode with `db_explain`.

For AptevaDB realtime updates, inspect `db_realtime_describe`. A database manager enables capture with `db_realtime_configure` (`enabled:true`); disabling ends resume history. Call `db_changes` with explicit `collections`, `waitMs` up to 20000, and optional initial `{op:"find"|"count"|"aggregate",args:{collection,...}}`. Repeat with returned `after` cursor and omit initial. Both database.read and database.watch are required. Events invalidate collections rather than supply individual row values; rerun the query after an invalidation. Expired/invalid history needs a fresh snapshot. Never treat progress events as record writes. Calling-app namespaces stay private. SQLite/Pebble change capture is not implemented yet.

## Joined reads

Use `joined_page` to page joined rows at a chosen grain, `joined_aggregate` for metrics over them, and `related_read` for parents with a bounded list of related rows. Every join needs an index on a related field. Database applies only the filters you pass, so include access and same-centre conditions yourself, such as an extra `{local:"root.centre",foreign:"centre"}` pair. See the README's "Relational reads".


## Dependent calculations and related records

Use `db_aggregate` for every summary, including joined or dependent calculations.
An ordinary request uses collection/where/groupBy/metrics. Relational requests
also accept joins, computed, filter, distinct, pick, branches and rollup, with
alias.field references. Optional lookups and timezone buckets enrich the final
input before grouping. Conditional metric filters, exact distinct counts and
explicit rounding retain their semantics.

For dependent work, supply `pipeline: {version: 1, nodes: [...], from: name}`.
Nodes are named scan, compute, join or aggregate stages. Top-level groupBy and
metrics perform the final aggregation over the selected relation. Use
`db_find` with the same pipeline for calculated records, adding where/select/
orderBy/limit/cursor and optional count:true. Put scans and calculations inside
the pipeline rather than mixing a collection or relational shorthand beside it.
See READS.md and testdata/dependent-aggregate.json for the complete contract and
an example that joins period rates back to individual sales before summing.

Use `db_find` with joins/computed/distinct/pick/group for joined paging, or with
relation and match for bounded related records. `match` only tests related rows
kept after their ordering and limit. Ordinary finds keep plain record fields;
explicit joins use alias.field names. Legacy db_query, db_joined_aggregate,
db_joined_page and db_related_read remain compatibility aliases; use the core
verbs in new integrations.

Put caller-derived eligibility/tenant filters on every independent scan. Keep
rate-calculation periods separate from display filters on valued sales. Use
cardinality: many_to_one when joining a rate to facts so duplicate keys fail.
Keep money in checked integer cents and round at the intended stage. Intermediate
budget exhaustion fails the whole read. Final aggregate limits only group output,
never metric inputs; never sum one limited output page to infer a global total.
Read-only batches of canonical operations share a snapshot and need read access.
