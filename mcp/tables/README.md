# Tables (v0.1)

Typed-row database for Apteva agents and human teams. The row-shaped
sibling to the `storage` app.

The app may be installed globally, but its data is never global: every
table and row is resolved against the calling project_id.

## Surfaces

- **17 MCP tools** — `tables_create`, `tables_list`, `tables_describe`,
  `tables_alter`, `tables_drop`, `indexes_create`, `indexes_list`,
  `indexes_drop`, `rows_insert`, `rows_get`,
  `rows_upsert`, `rows_update`, `rows_delete`, `rows_search`,
  `rows_count`, `rows_aggregate`, `tables_query`
- **Strict typed columns** — `text`, `number`, `bool`, `datetime`,
  `json`, `file_id` (FK into the `storage` app)
- **Read-only SQL escape hatch** — `tables_query` runs on a SQLite
  read pool, requires `{table_name}` placeholders for
  user tables, blocks internal tables, and enforces time/row/byte caps
- **Concurrent reads** — reads use a four-connection read-only pool while
  writes remain serialized; schema metadata is cached per project and table
- **Composite indexes** — validated column-based indexes can be created,
  inspected, and dropped without exposing physical SQLite names
- **Skill** — `how-to-use-tables` (`/tables`)

## Reserved columns

Every physical table gets `id`, `created_at`, `updated_at`, `_revision`. The user
can't declare or write to these directly.

## Identifier rules

Table + column names must match `^[a-z][a-z0-9_]*$` and be ≤ 64 chars.
Generated SQL only uses identifiers that pass this validation. Raw SQL
queries additionally require table placeholders and run in SQLite
`query_only` mode.

## Local development

```bash
cd mcp/tables
env GOWORK=off go build .
APTEVA_PROJECT_ID=test DB_PATH=/tmp/tables.db ./tables
curl http://localhost:8080/health
```

## Out of scope for v0.1

- Cross-app `file_id` validation on insert (just stores the integer;
  hydration is best-effort on `rows_get`)
- Expression indexes, partial indexes, and FTS. Composite indexes are
  column-based; upsert keys are automatically backed by managed unique indexes.


## 0.1.15 hardening

See [CHANGELOG.md](CHANGELOG.md) for compatibility and upgrade notes, and
[VALIDATION.md](VALIDATION.md) for the checks and measured results.

- Use `tables_list` with `summary: true` for navigation. Both list modes
  paginate with `limit` (default 100, maximum 1000), `offset`, `has_more`,
  and `next_offset`; schema pages also have a byte budget.
- Use `rows_search` with `include_total: false` and `select` for browsing.
  Pass the returned `next_cursor` as `cursor` without changing the filter,
  ordering, table or project. Cursors expire when the schema changes.
  They support ascending/descending sorts, ties and null values. They are
  continuations, not snapshots: changes to a row's sort value can move it.
- When editing, send the fetched `_revision` as `expected_revision` and the
  described table's `id` as `expected_table_id` to `rows_update` or single-row
  `rows_delete`. Stale rows or replaced tables return HTTP 409. The UI sends
  both checks and only the fields actually edited.
- `contains` searches literal text, including `%`, `_` and backslashes.
  Use `like` for intentional SQL patterns. Datetimes are stored in UTC with
  fixed nanosecond precision, including reserved timestamps.
- `json` preserves numeric literals through backend and browser round trips.
  `number` deliberately remains an IEEE-754 double. IDs and file IDs accept
  decimal strings for exact int64 values; unsafe floating-point IDs are rejected.
- SQLite authorization checks compiled table/index access as well as enforcing
  read-only connections. Raw physical/internal table names and virtual tables
  are inaccessible; JSON scalar functions, CTEs, joins and window functions work.
  Duplicate output labels require explicit aliases.
- `indexes_create` and automatic upsert indexes share a 64-index budget.
  `indexes_drop` needs `confirm: true`; removing a managed uniqueness constraint
  additionally needs `release_managed: true`. A later upsert recreates it and
  rejects existing duplicates.
- Binding Storage is optional. Hydration requires `platform.apps.call` approval
  and the Storage binding. `rows_get(hydrate_files: true)` returns per-column
  `file_hydration` status; failures retain the original integer. Lookups are
  deduplicated and bounded. Hydration does not validate file existence on insert.

### Budgets and events

Reads, writes, schema locks and connection queues honor request cancellation.
`max_write_ms` defaults to 30000; `max_batch_bytes` to 8 MiB; and
`max_tables_per_project` to 1000. Existing row/value/query caps remain active.
Result budgets count serialized rows including escaping; envelope/cursor
metadata adds a small overhead. Oversized first rows return HTTP 413 with a
projection hint. Oversized update responses roll back the transaction.

Events include inserted/updated IDs and current schema information. They remain
best-effort UI invalidations: reconnect, focus and online events refresh the
source of truth. Reliable workflow delivery would require a transactional outbox
and consumer deduplication; that conditional architecture change is not part of
this UI/database hardening release. Project-wide disk quotas likewise remain a
separate platform capacity policy.

### UI development

```sh
cd ui
bun install --frozen-lockfile
bun run typecheck
bun test
bun run build
```

Type checking uses the workspace's sibling `ui-kit`, matching the dashboard.
The build emits all four tracked browser bundles and source maps. React and
`@apteva/ui-kit` remain supplied by the dashboard import map.


### Exact MCP numbers

Tables pins App SDK v0.74.1 and opts into its `PreserveJSONNumbers()` capability.
Large numeric input literals reach tool handlers without being rounded by the
MCP decoder. Normal release builds and HTTP/MCP smoke tests use this published
SDK dependency directly, with no local module replacement.

`rows_update` also accepts a `select` projection for its returned row, including
through HTTP's `?select=id,_revision`. This allows small edits to otherwise very
wide rows. Update events carry that returned projection plus the changed fields.
Expanded default values count toward `max_batch_bytes`, not only caller input.
