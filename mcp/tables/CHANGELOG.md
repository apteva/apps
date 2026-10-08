# Tables v0.2.11 — SQL verification reuse and watched dependencies

- Cache successful ordinary SQL program verification before its early return, keeping caller/project checks on every request, bounded storage and schema/index/projection invalidation. Keep in-flight verification tied to its original epoch.
- Add optional `source_dependencies` / `watched_columns` on projection creation and description. Metadata-only/no-op updates do not dirty configured versions; relevant same-scope values, old/new scope moves, inserts/deletes and mappings retain durable coalescing and freshness.
- Persist affected version IDs in source transactions through additive migration 015. Existing definitions and pending legacy changes keep conservative behavior; startup automatically recreates capture triggers.
- Validate watch lists and require known scope/mapping inputs. SQL authors remain responsible for including calculation/filter/join inputs; configuration changes use immutable replacement versions.
- Add cache security/lifecycle tests, watched dependency aggregate/mapping/burst/restart/in-flight/migration regressions and a cold/warm verification benchmark. Pin SDK v0.97.0, verified by commit ancestry.

# Tables v0.2.10 — filtered live diagnostics

- Add errors-only, timeout and cancellation filters, with 10/25/50 row limits
  (10 by default) in both the panel and dashboard widget. Keep narrow widget
  metrics in two columns and truncate long row details with hover text.
- Replace manual refresh and 10-second polling with project/install-filtered
  SSE invalidations after diagnostics are saved. Coalesce bursts, retain a
  follow-up refresh during requests, and reconcile after reconnect/focus.
- Abort obsolete requests and retry failed fetches with bounded backoff.
- Establish a verified baseline before testing projection refresh deadlines;
  stop the mixed-workload worker gracefully and assert every aggregate count.

# Tables v0.2.9 — compact diagnostics layout

- Keep diagnostic table headings and values in responsive columns even when
  the host dashboard does not ship the app's arbitrary Tailwind grid utility.
- Reduce panel padding and diagnostic row height so more operations remain
  visible in both the full Tables panel and the dashboard widget.

# Tables v0.2.8 — durable read diagnostics UI

- Persist redacted slow, failed, timed-out, and canceled read diagnostics with
  bounded per-project retention.
- Add a project-panel Diagnostics surface and a suggested dashboard Home
  widget with error/slow counts, recent p95 duration, phase timing, and
  refresh controls.
- Serve diagnostics through the authenticated `GET /diagnostics` route without
  exposing SQL, parameters, row values, or raw database error text.

# Tables v0.2.4 — automatic projection migration

- Upgrade projection storage automatically during startup.
- Preserve each `p_<id>` result table as a writable compatibility surface for
  the previous worker, while current readers use atomic `pv_<id>` generation
  views.
- Materialize the compatibility surface when upgrading databases that already
  contain the 0.2.x result views.
- Declare the database upgrade backward compatible so the platform can perform
  a normal blue-green activation and safely restart the previous version if
  startup fails.

## 0.2.0 — Scoped analytics refresh and safe publication

- Bind validated scope parameters inside saved SQL, including timezone-aware day bounds. Map dependencies and old/new derived scopes through project-specific rules.
- Persist per-projection minimum refresh intervals, coalesced invalidations, forced requests and retry backoff across restarts.
- Manage result indexes, including indexes on building versions, using the existing index tools. Preserve indexes through publication and protect results from row writes.
- Report relevant source changes, consumed and published watermarks, requested-scope readiness, pending/running work, successful publication, failures and published coverage. Query results include snapshot-consistent metadata; read batches can inspect scoped status.
- Process building versions alongside current readers and reject activation until the complete build and relevant changes are published. Failed replacements preserve the current version.
- Reserve one background read connection and serialize refreshes. Enforce the calculation/scan/publication deadline, total result caps, bounded writer batches and short atomic switches; reclaim old generations in bounded batches.
- Migrate legacy results atomically while retaining their rows until rebuilding.
- Retain SDK v0.95.0 and SQLite v1.50.0; verify SDK tag ancestry. Add joined-aggregate, event-burst, restart, readiness, failure, cancellation, concurrency, index and migration coverage, plus fixed-size file-backed benchmarks.

## 0.1.27 — Persistent SQL projections

- Add versioned, project-scoped read-only SQL projections with explicit source dependencies and typed result schemas.
- Capture source inserts, updates and deletes transactionally through SQLite triggers, retaining only declared scope columns.
- Coalesce affected scopes into a durable queue with cursor watermarks, fenced leases, exponential retry backoff and pause/resume controls.
- Build new versions alongside the active definition and switch readers atomically with `projections_activate`; gate management and inspection through declared permissions.
- Refresh projections asynchronously and publish complete scope results atomically through the normal `tables_query` placeholder interface.
- Add projection lifecycle tools, backlog/freshness status, rollback-safe change capture and end-to-end regression coverage.

## 0.1.21 — Legacy text default compatibility

- Prevent startup migration failure when old text-column defaults contain raw strings instead of JSON-quoted strings.
- Apply the same compatibility rule to cached schemas, table lists and uncached column loading.
- Preserve valid JSON types, legacy metadata, existing rows, indexes and resumable per-table upgrade behavior. Keep non-text parsing errors actionable.
- Add raw-default, typed-default, partial-upgrade, restart and opt-in database-copy preservation regression tests.

# 0.1.20 — Read diagnostics

Adds one detailed completion record for every failed or slow read, plus optional
`log_all_reads` for temporary full sampling. Records query fingerprints,
Function/Gateway request correlation, per-call IDs, phase timings, row counts,
effective read-pool state, deadline sources and overruns. Query literals,
parameters, result values and raw database error messages are omitted.

Preserves 0.1.19 Function correlation, 0.1.18 JSON query compatibility, 0.1.17
startup migration behavior, and SDK v0.76.0. No new database migration, read-pool
limit change, SQL timeout change, or driver update.

Includes regression tests and a standalone synthetic reproducer showing delayed
cancellation while the pinned driver computes a later result row. This release
reports that condition; a driver-level interruption fix remains separate. See
READ_DIAGNOSTICS.md for measurement instructions and the precise limitation.

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
