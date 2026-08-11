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

Every physical table gets `id`, `created_at`, `updated_at`. The user
can't declare or write to these directly.

## Identifier rules

Table + column names must match `^[a-z][a-z0-9_]*$` and be ≤ 64 chars.
Generated SQL only uses identifiers that pass this validation. Raw SQL
queries additionally require table placeholders and run in SQLite
`query_only` mode.

## Local development

```bash
cd mcp/tables
go build .
APTEVA_PROJECT_ID=test DB_PATH=/tmp/tables.db ./tables
curl http://localhost:8080/health
```

## Out of scope for v0.1

- Cross-app `file_id` validation on insert (just stores the integer;
  hydration is best-effort on `rows_get`)
- Expression indexes, partial indexes, and FTS. Composite indexes are
  column-based; upsert keys are automatically backed by managed unique indexes.
