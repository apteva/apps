# Tables (v0.1)

Typed-row database for Apteva agents and human teams. The row-shaped
sibling to the `storage` app.

## Surfaces

- **12 MCP tools** — `tables_create`, `tables_list`, `tables_describe`,
  `tables_alter`, `tables_drop`, `rows_insert`, `rows_get`,
  `rows_update`, `rows_delete`, `rows_search`, `rows_count`,
  `tables_query`
- **Strict typed columns** — `text`, `number`, `bool`, `datetime`,
  `json`, `file_id` (FK into the `storage` app)
- **Read-only SQL escape hatch** — `tables_query` accepts SELECT or
  WITH, with `{table_name}` placeholders, parameterised values,
  hard timeout + row cap
- **Skill** — `how-to-use-tables` (`/tables`)

## Reserved columns

Every physical table gets `id`, `created_at`, `updated_at`. The user
can't declare or write to these directly.

## Identifier rules

Table + column names must match `^[a-z][a-z0-9_]*$` and be ≤ 64 chars.
This is the only protection against unsafe SQL identifier injection,
since identifiers can't be parameterised.

## Local development

```bash
cd mcp/tables
go build .
APTEVA_PROJECT_ID=test DB_PATH=/tmp/tables.db ./tables
curl http://localhost:8080/health
```

## Out of scope for v0.1

- Dashboard UI panel (manifest declares it, surface lands in v0.2)
- Cross-app `file_id` validation on insert (just stores the integer;
  hydration is best-effort on `rows_get`)
- Indexes, FTS — sqlite default index is `id`; user-defined indexes
  arrive when somebody hits the wall
- Cross-project global tables — manifest allows global-scope installs
  but the v0.1 code paths key everything by project_id
