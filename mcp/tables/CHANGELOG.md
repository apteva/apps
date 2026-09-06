# 0.1.18 — JSON query compatibility

Allow SQLite's built-in `json_each()` and `json_tree()` in `tables_query`,
including correlated audio-array reconciliation queries. Authorization checks
connection-local compiled virtual-table identities against trusted built-in
probes and rejects persisted or temporary name shadows. Arbitrary virtual
modules, private/cross-project reads, writes and unsafe functions remain denied.
Statement, timeout, row and byte limits are unchanged. No schema migration,
SDK update or server update is needed for this fix.

Regression coverage includes the full reported reconciliation query, malformed
and empty JSON arrays, nested stored-file identities, latest retry selection,
module/name spoofing, cross-project inputs, connection pooling and cancellation.

# 0.1.16 — SDK authentication update

Pins App SDK v0.74.1, retaining v0.74.0's exact MCP numeric decoding and adding
the concurrently released signed-route authentication fix. Query parameters such
as `?sig=...` no longer bypass authentication on protected HTTP routes.
Tables v0.1.15 remains an immutable release. No database or UI changes.

# 0.1.15 — Tables hardening

Based on `tables/v0.1.14` (`c04ba353`). Pins published App SDK v0.74.0 for
opt-in exact MCP number decoding. Dashboard v0.34.2 contains the accompanying
project-aware navigation and event-stream reconnect fixes.

## Upgrade

Migration 005 records physical schema versions and a monotonic table identity.
On mount, each legacy table is rebuilt in its own transaction to preserve row
IDs, user columns, JSON text, and indexes while normalizing datetime cells and
adding a non-reusing row identity and `_revision`. A user column called
`revision` is retained. Legacy upsert indexes are registered from SQLite's
actual index columns. Unknown row counts are repaired in a writer transaction.

Use a database backup and allow disk space for the replacement table and WAL
before deploying. `migration_timeout_ms` defaults to five minutes and can be
raised to one hour for large installations. Invalid historical datetime values
stop the affected table's migration without replacing its original data. After
correcting those values, restart to resume; already-upgraded tables are skipped.
This migration was tested on local historical-schema fixtures, not production
records. Deleted IDs from before the upgrade cannot be reconstructed.

## Compatibility

- `tables_list` now defaults to 100 results with continuation metadata. Clients
  that assumed an unlimited response must paginate. `summary=true` omits columns.
- Reserved row timestamps and datetime values use fixed-width UTC text.
- `contains` treats wildcards literally; use the new `like` operator for patterns.
- Raw SQL rejects duplicate output labels, virtual tables, and unauthorized
  storage roots. Valid literals/comments no longer trigger false substitutions.
- Malformed optional arguments, fractional IDs and unsafe floating-point IDs
  return validation errors instead of being silently coerced.
- `_revision` is an additional reserved output field. Revision and table-identity
  preconditions are optional for existing API clients and always sent by the UI.
- Oversized first rows and oversized update responses return HTTP 413. Narrow
  reads with `select`; oversized updates do not commit.
- Existing installations need normal platform approval for the added app-call
  permission and an optional Storage binding before hydration can succeed.
- Ship the accompanying dashboard changes for project-aware card navigation
  and immediate refresh on shared event-stream reconnection.

## Performance and usability

Prepared statements and insert SQL shapes are reused per transaction. Insert
batches share one canonical timestamp. The dashboard uses lightweight table
summaries, a projected grid, cursor pagination, and no exact filtered count.
Refreshes are coalesced, stale resource requests are canceled, and table schema
locks no longer block independent tables. JSON budget accounting avoids a second
serialized copy of normal row/batch objects.

Editors retain invalid input with a message, distinguish null/default/empty
text, preserve large JSON numbers, and gate duplicate submissions. Dialogs have
focus trapping, Escape handling and bounded scrolling. API help generates valid
project/install-scoped requests. Card status matching uses exact vocabulary.
