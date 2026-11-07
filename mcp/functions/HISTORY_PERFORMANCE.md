# Project invocation history pagination

The project-wide `/invocations` endpoint uses the additive migration
`009_invocation_history_index.sql` to index `(project_id, id DESC)`. The existing
`(project_id, function_id, id DESC)` index orders calls within one function;
it cannot order a whole project's history by ID without a temporary sort.

History reads use the SDK’s separate read pool, avoiding its serialized writer
connection, and run two bounded queries in one read transaction:

1. Select at most 200 invocation IDs in descending order, with project,
   function/status filters and an optional `id < cursor` boundary. Exclude
   mismatched function/project rows before applying the limit.
2. Fetch summary fields and resource/identity JSON only for those IDs. Both
   telemetry tables use primary-key lookups. Return rows in the first query's
   order without sorting payload-bearing rows in SQLite.

The read snapshot preserves page membership between queries, including during
retention, status updates, or function deletion. Cursor pages remain exclusive:
newer calls added after the first page cannot move the boundary or duplicate
previous rows. Each request retains the 15-second cancellation/deadline budget.
Error/status filters may still visit multiple index entries to find enough
matches; they do not load telemetry for discarded entries. Summary output,
stderr, and event bodies stay omitted; full output is fetched by invocation ID.

## First index creation: maintenance window required

SQLite has no concurrent index-build facility. Building this index scans all
retained invocation IDs and temporarily holds the database writer lock. Starting
a replacement sidecar while the old sidecar serves traffic does **not** make
that operation safe: they share the same database, and old invocation writes can
block or fail while the migration runs.

For each populated installation, schedule the first application in a maintenance
window. Do not use a rolling/hot upgrade to create this index on a busy database.

1. Back up the database using the existing consistent backup procedure. Check
   free disk space for the index, journal/WAL, and SQLite temporary files. Rehearse
   the migration on a staging copy of that installation to estimate duration.
2. Pause all invocation producers, including Jobs schedules and inbound callers.
   Wait for invocations and deployments to finish. Stop **all** Functions runtimes
   sharing this database, including staged or draining replacement processes.
3. Start the fixed version exclusively. The SDK applies migration 009 before
   `OnMount`, worker preparation, or app-route readiness. Its existing startup
   budget is 1,800 seconds; the index and migration receipt commit atomically.
   Cancellation or failure rolls back that transaction; never resume traffic
   against a partially successful startup. Allow an adequate maintenance window
   rather than assuming the normal hot-upgrade health interval will suffice.
4. Confirm readiness and inspect the index/query plans. Verify first and cursor
   pages return distinct descending IDs within the intended project, with no
   `USE TEMP B-TREE FOR ORDER BY` in the page-ID query. Then resume producers.

If staging rehearsal exceeds the startup budget, keep all runtimes stopped and
pre-create the same `CREATE INDEX IF NOT EXISTS` statement with the maintenance
SQLite tool before starting the app. Do not forge migration receipts; the SDK
will run the idempotent statement and record its own receipt on startup.

Once the index exists, subsequent upgrades do not rebuild it. `IF NOT EXISTS`
and the SDK's migration receipts make normal restarts inexpensive. This patch
changes Functions only; it does not change Telephony, agents, business handlers,
or deploy anything to Production.

## Reproducible checks

From `mcp/functions`:

```sh
GOWORK=off go test -run 'TestHistory(Page|Pagination|Index)' -count=1 -v
RUN_FUNCTIONS_HISTORY_PERFORMANCE=1 GOWORK=off go test -run '^TestHistoryMillions$' -count=1 -v -timeout=10m
```

The opt-in fixture contains two million invocations across two projects and
multiple functions. 4,600 invocations have 64 KiB resource JSON and 64 KiB
identity JSON apiece, including old off-page records and the recent pages.
Each measured page carries approximately 25 MiB of telemetry. The test reproduces
the temporary sort before migration, checks indexed first/cursor plans after
migration, measures 16 page reads, and rejects duplicate IDs or project leaks.
This is synthetic local validation, not a production latency guarantee.

## Measured local result (2026-10-09)

Two million invocations, 1.28 GiB database plus WAL; 16 reads of 200 calls
carrying roughly 25 MiB per page. Median read **24.2 ms**, p95/worst **34.6 ms**.
Index creation on the populated fixture took **2.08 s** with producers stopped.
First-page and cursor query plans used `ix_inv_project_id` without a temporary
sort. Detail and telemetry lookups used integer primary keys. These figures are
machine-specific; rehearse index creation on the production-sized staging copy.

Full Functions Go suite passed (110.7 s), along with targeted history race tests
(3.1 s), `go vet ./...`, and diff checks. Coverage includes HTTP cursor responses,
read-pool routing, concurrent deletion snapshots, empty/project-scoped pages,
status/function filters, cancellation, and index rollback/write-lock behavior.
