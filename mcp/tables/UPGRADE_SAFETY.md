# Tables 0.1.17 migration startup fix

This change addresses the Tables 0.1.14 → 0.1.16 failure reported on Apteva
0.50.1. The startup contract conflict is confirmed in code: synchronous mount
migration could run for five minutes, while the supervisor waited one minute.
The exact slow statement in the reported deployment has not been established. No customer
host, database, deployment, or backup was accessed or modified for this work.

## Data compatibility and resume contract

For storage-version-zero tables, migration now uses additive schema changes:

- Add `_revision` with a constant default, without rebuilding the table.
- Track row identity in separate metadata, with an insert trigger that also
  observes writes from 0.1.14. New-version inserts allocate monotonic IDs.
- Add an update trigger so writes from 0.1.14 advance `_revision` as well.
- Reconcile existing index metadata, initialize missing row counts, and commit
  the storage-version marker in the same per-table transaction.

Stored rows, timestamp encodings, IDs, physical table roots, and index definitions
are preserved. Published SQL migrations 001–005 are unchanged. Migration 006
adds `legacy_storage` and `row_identity` metadata. A failed current transaction
rolls back completely; completed tables and SQL migration files remain committed.
Retry skips completed tables. Writer contention is retried within the mount's
absolute cancellation budget, with progress exposed in health and logs.

The replacement serves no application traffic until mounting succeeds. During
this window, the old 0.1.14 process can continue normal CRUD against the shared
database. Its exact datetime equality also continues to work after committed
additive changes. New writes to legacy tables retain 0.1.14 timestamp encodings;
new API reads, filters, ordering, cursors and aggregates normalize datetimes at
the API/query boundary. Invalid historical dates remain readable for repair.
Canonical tables successfully migrated by 0.1.15/0.1.16 remain unchanged.

This is a compatibility contract for these additive migrations, not a guarantee
that arbitrary future schema changes or new-version features can be downgraded.
It does not undo timestamp rewrites committed by an earlier 0.1.16 migration.
A legacy user column named `_revision` is rejected with a clear error rather than
overwritten.

Legacy datetime normalization can require scanning a datetime index instead of
seeking directly. Other indexed keys and ID pagination retain their existing
paths. Raw SQL queries still expose the preserved storage representation.

## Startup and failed activation

Tables declares a 600-second startup budget. Its own migration timeout remains
300 seconds by default; the effective timeout is the smaller remaining SDK/app
budget. Raising only `migration_timeout_ms` cannot override the platform cutoff.
The new SDK serves HTTP 503 `initializing` before database setup and mounting,
reports progress, and publishes HTTP 200 `ready` only after successful mount.
All application routes are gated until then. SIGTERM and deadlines cancel SQL
work. A short SQLite startup writer wait bounds cancellation latency; runtime
writer settings are restored after mounting.

The platform honors the explicit startup budget without extending it on each
progress update, distinguishes initialization failure from an ordinary health
retry, detects early process exit, and reports progress in supervisor logs.
Failed activation retains/restarts and verifies the prior process. Its error
explicitly states that committed database changes were not restored.

Apps declaring `requires_restore` are rejected by automatic activation. Such
upgrades need a separate offline procedure: stop all database writers, take and
verify a consistent backup, migrate and validate, and restore only while all
writers remain stopped if validation fails. Never restore over a live fallback
process. A binary health probe is not proof of database compatibility.

## Validation

- Full Tables tests with race detector, including atomic table rollback,
  partial completion/resume, canceled work, legacy revisions, invalid date
  preservation and existing query/cursor/write regressions.
- Full SDK tests with race detector, including real HTTP initialization gating,
  mount errors, deadlines, shutdown cancellation, SQL migration cancellation,
  rollback of DDL and receipts, and restoration of foreign-key settings.
- Full server tests plus race-tested activation regressions: extended manifest
  deadline, initializing never accepted as ready, fixed absolute deadline,
  verified old-process fallback, and rejection of restore-required activation.
- `tests/upgrade_startup.py` runs real 0.1.14 and candidate processes against a
  synthetic 620,204,032-byte SQLite database with 55 tables and 100,000 call
  records. It reproduces already-committed migration 005, cancels a replacement
  while a writer lock is held, overlaps old-version CRUD with migration,
  verifies old datetime equality against new writes, stops the candidate to
  exercise fallback, then remounts and checks IDs, row hashes, index roots,
  integrity and foreign keys.

Latest local result: 0.122 seconds to readiness, 1.036 seconds to cancel
under writer contention and 0.023 seconds to remount. Across successful runs,
indexed key lookup took 1–8 ms and datetime count across 100,000 rows took
48–67 ms. These are synthetic local
measurements, not a Scaleway timing guarantee or a benchmark of the exact
customer workload.

Run the process regression after building both binaries:

```sh
python3 tests/upgrade_startup.py \
  --old-binary /path/to/tables-v0.1.14 \
  --old-migrations /path/to/v0.1.14/migrations \
  --candidate-binary /path/to/tables-candidate
```

## Release order and operational state

Tables 0.1.17 pins SDK v0.76.0, which includes the v0.75.0 and v0.74.1 fixes.
The app source is pinned to the immutable `tables/v0.1.17` tag. Dashboard sources
were not changed.

The SDK and Tables are released separately from the server. The Apteva release
version is prepared as 0.50.2, but no server release or deployment is part of this
release. The server startup changes remain prepared in
`codex/server-migration-startup` for that separate release.

On Apteva 0.50.1 and earlier, the existing platform startup cutoff still applies:
the new manifest budget cannot extend it. The additive migration avoids the
large row-copy cost, but successful local timings are not a guarantee that every
upgrade under contention finishes within the old cutoff. Keep the customer
rollout paused until the server change and a consistent staging-copy rehearsal
are complete.

Publishing these releases does not deploy them or resume a paused rollout.
Validate backup consistency and compatibility against the affected staging
database before resuming upgrades; synthetic local tests do not substitute
for that deployment-specific check.
