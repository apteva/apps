---
name: how-to-use-tables
command: /tables
description: Tables app mental model — when to pick tables vs storage vs the domain apps, schema discipline, query predicates.
---

# Working with the `tables` app

`tables` is a typed-row database. Use it when you need to record
structured data you'll later query, sort, or aggregate.

## Pick the right app

- **`tables`** — ad-hoc shapes you invent on the spot ("a list of
  books I'm reading", "log every workout set", "track tax-deductible
  receipts"). Schema is yours.
- **`storage`** — files. Bytes, not records. Use a `file_id` column in
  a `tables` table to link rows to attachments.
- **`crm`** / **`tasks`** / **`todo`** — purpose-built for those
  domains. Don't reimplement them inside `tables`.

## Column types

`text`, `number`, `bool`, `datetime` (RFC3339), `json` (any JSON
value), `file_id` (foreign key into `storage`).

## Reserved columns

Every table gets `id`, `created_at`, `updated_at`, `_revision` for free. Don't
declare them, don't supply them on insert, don't try to update them.

## Identifier rules

Table + column names must match `[a-z][a-z0-9_]*` and be ≤ 64 chars.
Lowercase, snake_case, no spaces.

## The atomic-insert contract

`rows_insert` rejects the entire batch on first failing row. Plan for
all-or-nothing: validate your data shape before sending, and don't
expect partial success.

## When to drop to `tables_query`

The named tools cover insert / update / delete / search / count. Reach
for `tables_query` only for SELECT-shaped questions the typed tools
can't express:

- aggregations (`SELECT category, SUM(amount) FROM {expenses} GROUP BY 1`)
- joins (`SELECT b.title FROM {books} b JOIN {authors} a ON ...`)
- DISTINCT, window functions, CTEs

Reference user-tables with `{name}` placeholders — the app substitutes
the physical table name. Bind values via `?` + `params`, never inline.
The query is timed-out and row-capped.

## Fast search and pagination

`rows_search` returns an exact `total` by default for compatibility. Set
`include_total: false` when the caller only needs the current page; Tables
then skips `COUNT(*)` and returns `has_more` from the `limit + 1` query.

Create composite indexes for recurring filters and sorts. Put equality
filters first, followed by range or ordering columns:

```json
{
  "table": "records",
  "name": "status_expiry",
  "columns": ["status", "expires_at", "id"]
}
```

Use `indexes_list` to inspect indexes and `indexes_drop` with `confirm: true`
to remove user-managed indexes. Unique indexes reject creation when existing
rows contain duplicate keys. `rows_upsert` creates and owns its unique indexes
automatically.

## File-backed rows

Set `hydrate_files: true` on `rows_get` to swap each `file_id` integer
for `{id, url, expires_at}` — the URL is a signed time-limited link
the user can open in a browser.


## Safe editing and bounded results (0.1.15)

Fetch `_revision` before editing and pass it as `expected_revision`; pass the
schema's table ID as `expected_table_id` to protect against a dropped/recreated
table with the same name. Patch only changed fields. A 409 means reload and
reconcile; do not blindly retry a stale write. Omitted fields keep their value;
explicit null requires a nullable column. Defaults apply to omitted inserts.

Use `next_cursor` as `cursor` for the next search page, keeping filters and
ordering unchanged. `next_offset` is also available and advances by rows
actually returned. Never advance a byte-truncated page by the requested limit.
Select fewer columns if a row exceeds the byte budget. Cursor results are not
frozen snapshots; sort-key edits may move records between pages.

`tables_list` defaults to pages of 100 tables. Follow `has_more` / `next_offset`.
Use `summary: true` to omit schema/default payloads and describe tables on demand.

`contains` is literal; `like` explicitly accepts SQL wildcard patterns.
For integers beyond 2^53-1 use decimal strings in ID/file_id inputs. JSON numeric
literals remain exact; the `number` column type is a floating-point double.

File hydration needs the optional Storage binding and app-call permission.
Inspect `file_hydration` for per-column errors; an unresolved integer is not a
successful URL resolution. Upsert uniqueness indexes count toward the shared
64-index cap. Removing one requires `release_managed: true` and `confirm: true`.

## Persistent analytics projections (0.2.0)

Use projections for repeated SQL analytics. Declare source_tables, result_columns
and scope_columns. Source writes capture changes transactionally; the worker
coalesces scopes and publishes complete generations. Results stay read-only.

For joined/materialized CTEs, supply scope_sql with predicates inside the CTE and
scope_params naming bound scope columns; add indexes to source lookup fields.
Default outer filtering can still calculate historical data. Use scope_rules for
derived day/month scopes with an explicit IANA timezone, or generic dependency
SQL with params taken from both old/new source fields. Return DISTINCT scopes.
Timezone-aware start/end day parameters use normalized UTC timestamps and handle
DST; full SQL must implement equivalent grouping.

Set min_refresh_interval_seconds (for example 30), inspect it through
projections_describe, or update it through projections_update. Dirty deadlines
persist and don't slide under continuous changes. Administrative force:true on
projections_refresh bypasses the interval while retaining limits and pause.

Manage result indexes using indexes_create/list/drop and optional version.
Inspect projections_status with an optional scope. Compare published_change_id
with relevant changes, and use ready/stale, pending/running work and published
coverage_from/to. Consumed changes alone do not make results ready.
Tables_query includes projection metadata from the same snapshot as rows; a
read_snapshot batch can combine scoped projections_status with the data query.

Build replacements with activate:false, create their indexes, wait for ready:true
and activate explicitly. A failed build leaves the current version usable.
Coverage is the declared half-open SQL window, published with the successful
generation. Applications decide whether to use a covered zero or fallback query.
See PROJECTIONS.md for examples, limits and the restore-required downgrade policy.
