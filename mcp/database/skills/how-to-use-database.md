# Database app

Use this app for local structured data shared through a portable record contract. First discover databases with `db_databases_list`, then collections with `db_collections_list` and their schema with `db_collection_describe`. Every data call selects a `database` (default: `default`) and a `collection`. Several databases and collections are supported. SQLite and Pebble use the same arguments.

Create the database and explicit collection schema before writing. Undeclared fields are rejected. Integers are decimal strings, booleans are JSON booleans, and datetimes are RFC3339 with at most microsecond precision. Nullable fields may be omitted or null. Primary keys default to generated UUID `id` values; read the schema for custom/composite keys.

Use `db_find` with typed `{field,op,value}` predicates, optional `and`/`or`, `select`, `orderBy`, and `limit`. Follow `nextCursor` with the same query. Use `db_get` with the exact primary-key object when known. Null comparisons use `is_null`/`is_not_null`, without a value. Do not assume field names or invent IDs.

Use `db_index_create` with `index:{name,fields:[{field,direction}],unique}` for repeated equality/range/sort queries. Compound field order matters. Put equality predicates first. `db_explain` shows the execution path. Indexes are created explicitly; no indexes are created automatically while reading. A failed unique-index build leaves existing data unchanged.

Use `db_aggregate` for summaries rather than fetching one page and computing totals. Arguments: `where`, `groupBy` (field names), `metrics:[{name,op,field?}]`, `orderBy`, `limit`. Operations: `count`, `sum`, `avg`, `min`, `max`. `count` without a field counts records; with a field it counts non-null values. Metrics ignore nulls. Integer sums/counts are decimal strings. Limits apply to output groups; work-limit errors never mean partial totals are exact. No grouping produces one summary row.

Use `db_insert` with a records array; `db_upsert` uses primary keys or a named unique `conflictIndex`. `db_update` uses `set`/`increment` and `key` or `where`. Use `ifVersion` with a key to protect a read-modify-write operation. Bulk updates/deletes need an explicit `maxAffected` bound (default 1); full-collection mutations require `all:true`. Deleting databases, collections, or indexes requires `confirm:true` and appropriate user intent.

`db_batch` atomically executes record writes across collections in the same database using `operations:[{op,args}]`. It cannot span databases or run schema operations. Do not automatically retry writes after an uncertain network response: request-id deduplication is not implemented yet. Resolve state first with a known key or unique index.

Calling apps have private database namespaces. Project-owned databases are separate. Cross-project access, cloud adapters, joins, raw SQL, schema alteration, backups, and shared bindings are not available in this initial version. Large Pebble scans and synchronous index builds have explicit work limits; prefer indexed queries and create indexes before large imports.
