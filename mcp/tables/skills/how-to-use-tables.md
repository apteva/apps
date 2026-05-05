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

Every table gets `id`, `created_at`, `updated_at` for free. Don't
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

## File-backed rows

Set `hydrate_files: true` on `rows_get` to swap each `file_id` integer
for `{id, url, expires_at}` — the URL is a signed time-limited link
the user can open in a browser.
