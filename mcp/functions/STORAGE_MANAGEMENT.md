# Functions storage management (v1.18.0)

This change addresses retained SQLite free pages and oversized invocation detail.
It does not deploy, compact, or change retention in an existing production install.

## Behavior and limits

New database files start in `auto_vacuum=INCREMENTAL` before the SDK enables WAL
or creates the schema. Creation publishes an initialized empty file atomically;
concurrent startups never overwrite an existing file. Existing files retain their
mode. Mode `NONE` reports `conversion_required`; mode `FULL` already reclaims
pages automatically and is left unchanged.

A dedicated maintenance loop runs every five seconds, separately from worker
reaping and artifact retention. Each pass has a 100 ms context budget and a
separate connection with a 10 ms SQLite busy timeout and 2 MiB page cache.
It processes at most 32 successful detail rows, 32 failure detail rows, 32
summary deletions, and 32 incremental-vacuum pages. The starting phase rotates
each tick so an exhausted budget or blocked detail phase cannot permanently
starve summary deletion or reclamation. No automatic full `VACUUM`,
`TRUNCATE` checkpoint, or startup conversion runs against an existing database.

Reclamation requires no locally active work and no invocation/build registered
by any runtime sharing the database. The cross-runtime check and vacuum share
one short `BEGIN IMMEDIATE` transaction. New invocation registration may briefly
wait for that transaction; active work is never killed or drained to reclaim
space. A busy lock or expired budget defers remaining work until the next tick.
Transactions release their locks on cancellation/error. Retention excludes
`running` rows, including old calls awaiting restart recovery.

The page bound limits pages removed, not all physical bytes written: SQLite may
move other pages and update indexes. Context deadlines are cooperative and do
not impose kernel CPU or I/O quotas. The five-second cadence limits duty cycle;
configure sidecar/container CPU and I/O quotas if strict resource ceilings are
required. Multiple overlapping runtimes each have their own budget.

On eligible idle ticks, a non-waiting PASSIVE checkpoint is attempted only
if at most 8 MiB of WAL frames remain uncheckpointed. SQLite NOOP checkpoint
status reads the outstanding frame count without copying pages; the physical
file's historical high-water length does not permanently disable reclamation.
Checkpointing is retried even when a previous vacuum exhausted all free pages.
A larger outstanding backlog defers checkpointing to the normal SDK writer or
operational maintenance and emits `checkpoint_deferred` in health alerts. Long
read snapshots can pin a WAL and delay physical shrinkage. The loop does not
force readers out or truncate the WAL. Free pages remain reusable while vacuum
is skipped, including on continuously busy installations.

## Retention

Defaults retain execution summaries for 30 days, successful detail for 7 days,
and failure detail for 30 days. All non-`ok`, non-`running` statuses use failure
retention, including canceled and timed-out calls. Detail expiry clears stored
request/response previews, console logs, error text, resource-call JSON, and
identity JSON atomically. Summary IDs, project/function/version, status, exit
code, timing fields, and config digest survive until summary retention expires.
Performance rankings continue to use those summary timing fields.

Detail and list responses expose `details_expired`; `functions_logs` also returns
it. The detail panel explains when bodies/logs have expired. Expiry cannot be
undone by lengthening retention. To preserve the previous detail policy, set
both detail retention settings equal to summary retention **before upgrading**.

Cutoffs use midnight UTC to compare both legacy SQLite timestamp strings and
RFC3339 timestamps safely. Consequently records can survive up to one extra day
beyond the configured duration. Partial indexes remove already-expired rows
from future detail sweeps; the indexed batch queries do not repeatedly scan all
old cleared payloads. Running rows that finish later become eligible normally.

| Environment variable | Default | Accepted values |
| --- | ---: | --- |
| `APTEVA_FUNCTIONS_INVOCATION_RETENTION_DAYS` | 30 | 1–3650 |
| `APTEVA_FUNCTIONS_SUCCESS_DETAILS_DAYS` | min(7, summary days) | 1–summary days |
| `APTEVA_FUNCTIONS_FAILURE_DETAILS_DAYS` | summary days | 1–summary days |
| `APTEVA_FUNCTIONS_RETENTION_BATCH_ROWS` | 32 | 1–128 per phase |
| `APTEVA_FUNCTIONS_VACUUM_PAGES` | 32 | 0–1024; 0 disables incremental reclamation |
| `APTEVA_FUNCTIONS_STORAGE_BUDGET_MS` | 100 | 10–1000 |
| `APTEVA_FUNCTIONS_STORAGE_INTERVAL_SECONDS` | 5 | 1–3600 |
| `APTEVA_FUNCTIONS_STORAGE_WARN_GIB` | 10 | 1–1024 |

Invalid/out-of-range values fall back to defaults. These settings apply to an
installation, including every project served by a global installation. At the
default batch/cadence, each retention phase can process at most 6.4 rows/second;
actual throughput can be lower when the budget expires or the writer is busy.
Monitor backlog and rehearse larger batches/budgets on staging before tuning.

## First upgrade: maintenance window required

Migration `010_storage_retention.sql` adds the expiry flag and builds two partial
indexes over historical invocation rows. SQLite index creation holds a writer
lock and scans existing history, even when most rows have no payload. Just like
migration 009, its **first application requires all runtimes and producers to be
stopped**. Rehearse it on a production-sized staging copy; do not roll this
migration onto a busy shared database.

For every database file/installation separately:

1. Inventory the exact database path and every process/container sharing it.
   Pause Jobs schedules, inbound HTTP/API Gateway calls and other producers.
   Wait for active invocations/builds to finish and stop **all** runtimes,
   including staged, draining, and recovery processes.
2. Take a consistent backup using the established backup procedure. Keep it
   outside the working database path. Check available disk space and rehearse
   conversion plus index creation on a copy. In-place `VACUUM` can require free
   space up to twice the original database size; do not attempt it on a nearly
   full volume without first adding space or planning a copy-based procedure.
3. Inspect mode with the SQLite CLI. For mode `0` only, run this sequence with
   the correct path, while every runtime remains stopped:

   ```sh
   sqlite3 "$functions_db_path" 'PRAGMA wal_checkpoint(TRUNCATE);'
   ```

   Check the first checkpoint's result before continuing: its first value must
   be `0` (no busy readers/writers). If it is nonzero, keep runtimes stopped and
   find the remaining connection. Then run:

   ```sh
   sqlite3 "$functions_db_path" <<'SQL'
   .bail on
   .timeout 1000
   PRAGMA auto_vacuum=INCREMENTAL;
   VACUUM;
   PRAGMA wal_checkpoint(TRUNCATE);
   PRAGMA auto_vacuum;
   PRAGMA quick_check;
   SQL
   ```

   Set `functions_db_path` to the inventoried file beforehand. The CLI must
   return mode `2` and `ok`; inspect checkpoint results for busy readers/writers
   and stop if any are reported. This sequence uses no explicit transaction and
   exits before recovery/readiness work. Do not hold a transaction/connection
   from a maintenance script while waiting for app recovery. Preserve mode `1`
   databases unless intentionally switching to incremental mode in maintenance.
4. Start the new version exclusively, with producers still paused. SDK migration
   010 builds its indexes and records its receipt atomically before readiness.
   Its existing startup timeout is 1800 seconds; rehearse index duration. If
   startup fails or times out, keep producers paused and investigate. Do not
   forge receipts or resume traffic against partially successful startup.
5. Verify readiness and `GET /storage?project_id=<id>` (or `functions_storage`).
   Check mode, physical DB/WAL bytes, reusable bytes, project backlog and oldest
   record. Verify normal invocation/detail/history reads, then resume producers.
   Retention/reclamation catches up gradually; a large backlog is not an instant
   compaction request. Keep the backup until the upgraded installation is stable.

The app intentionally supplies no live conversion endpoint. Downgrading cannot
restore expired payloads; use the verified backup through the established
restore procedure if rollback needs the old detail data. No file replacement
should occur while any old process/SQLite connection still uses the database.

## Monitoring

`GET /storage?project_id=<id>`, `functions_storage` (with `_project_id` for global
installations), and the panel's **Storage and retention** section expose:

- Physical database, WAL and SHM lengths, SQLite page bytes and reusable bytes.
  These are **installation-wide**; project isolation applies to record metrics.
  Reusable bytes are whole free pages, not all fragmentation inside live pages.
- Reclamation mode and effective retention/maintenance settings.
- Project oldest invocation and number of distinct expired rows awaiting detail
  expiry or summary deletion. Counts stop at 1001 and set `backlog_capped`; the
  UI displays `1000+` instead of performing an unbounded exact count.
- Last maintenance counts/skip reason and last maintenance error, shared across
  the installation. They describe this process, not durable lifetime totals.

Structured warnings are logged at most hourly for disabled conversion,
configuration-disabled incremental reclamation, DB+WAL over the configured
threshold, at least 256 MiB and 20% reusable pages, WAL over 512 MiB, or more than
1000 expired rows, plus a deferred checkpoint backlog over 8 MiB. Maintenance failures appear in the health response and the
hourly log sample. The panel refreshes once a minute while expanded. Connect the
existing log-monitoring system to `functions storage alert` and its `code` field
for notifications; the app does not send email/messages itself.

The warning threshold is not a disk quota and does not forecast exhaustion.
Monitor host free disk alongside these metrics. Shortening retention without
conversion does not return pages to the filesystem. SQLite documents the
[auto-vacuum modes](https://www.sqlite.org/pragma.html#pragma_auto_vacuum),
[incremental-vacuum page bound](https://www.sqlite.org/pragma.html#pragma_incremental_vacuum)
and [VACUUM space requirements](https://www.sqlite.org/lang_vacuum.html).

## Verification

From `mcp/functions`:

```sh
GOWORK=off go test ./... -run '^TestStorage' -count=1 -v
GOWORK=off go test ./... -count=1 -timeout=15m
GOWORK=off go test -race . -run '^TestStorage' -count=1
GOWORK=off go vet ./...
```

Storage tests use real file-backed SQLite databases for mode persistence,
concurrent new-file creation, legacy preservation, partial index query plans,
transaction rollback/cancellation, retained summaries and later-completing work,
batch progress, actual incremental file shrinkage, cross-runtime active-work
protection, writer contention, bounded project-scoped backlog and HTTP/MCP access.
They are local checks, not a production load/latency guarantee.

Release validation (2026-10-25): full Functions Go suite passed (116 s) with
SDK v0.107.0, targeted storage race tests and `go vet` passed, and all 8 Functions
UI tests passed. Linux amd64 and Darwin arm64 binaries built successfully;
`go mod verify` passed. Compiled binary smoke tests covered fresh startup,
legacy NONE-mode upgrade and an offline-converted INCREMENTAL-mode upgrade,
including migration receipts/indexes, preserved history and SQLite integrity.
Regression checks cover a reused large WAL, checkpoint retry after the last free
page, fair progress across maintenance phases and atomic migration rollback.
Production data/settings/runtimes were not modified.
