# Startup recovery in Functions 1.11.2

Startup no longer updates every historical invocation or version whose status
looks unfinished. Migration 007 creates a small `function_active_work` table,
indexed by process owner. Recording an invocation or deployment and its owner
is atomic; terminal status and deletion triggers remove the active entry.
Recovery updates abandoned entries by primary key. It never creates a status
index over the historical payload tables during startup.

Each runtime holds a kernel file lock with a unique ID next to the database.
A replacement skips owners whose locks are still held, including an old
instance draining calls. Crashes release locks automatically. Restoring a
consistent database into a new directory/host recovers the abandoned work
because the original host's locks are absent. The maintenance loop rechecks
owners every 30 seconds to recover old processes that crash after a
replacement has mounted. Keep the database and its lock
directory on the same local filesystem visible to overlapping runtimes;
concurrently sharing one SQLite database across hosts is not supported.

Older versions did not record owners. The first mount snapshots their maximum
row IDs through indexed lookups. A background task waits six minutes (past the
old five-minute invocation and two-minute build deadlines), then reconciles
128 rows per transaction with a 100 ms pause between batches. Persistent
cursors make this resumable. Owned work and rows after the snapshot are
excluded. Unowned legacy rows can therefore remain visibly running until
this one-time reconciliation reaches them. It does not block readiness.

Legacy source snapshots are recovered for active versions in the background
before runtime preparation. This uses primary-key lookups instead of scanning
version history. Inactive legacy versions recover their cached immutable
source on explicit rollback. Ordinary invocation retention also uses batches
of 128 rows, at most one batch per 30-second maintenance tick; the configured
retention period is unchanged. This bounds transaction size, not disk space;
SQLite file compaction remains a separate maintenance operation.

## API and logs

Authenticated app routes (prefix with the platform's Functions proxy path):

- `GET /runtime/status`: `startup_steps` with `name` and `duration_ms`,
  `recovered_work`, `active_work`, `draining`, and the legacy recovery policy.
- `POST /runtime/drain`: stops accepting new invocations/deployments/rollbacks
  on this process and returns `draining`, `active_work`, and `drained`.
  Repeat to poll. In addition to normal app authentication, this operation
  requires a separate, per-process platform control credential; ordinary
  app callers cannot initiate it. It does not invoke business handlers and does not cancel
  existing calls. Draining is permanent for that process; use only after
  directing new traffic to a replacement.

Initialization reports owner registration, abandoned-work recovery, legacy
checkpoint initialization, and capacity loading separately, with timings and
recovered-row counts in logs. Background snapshot recovery logs its duration.

## Companion platform change (not included in this release)

For healthy HTTP sidecars without exclusive fixed ports, the platform keeps
the committed installation running while staging `pending_manifest_json`.
Binding checks and route reloads continue to use the old instance until the
replacement passes health checks. The existing list API's presentation status
remains `pending` for dashboard polling, with new `serving: true` and
`upgrade_in_progress: true` fields exposing the distinction to API clients.
The committed version remains visible until activation succeeds.

After switching routes, the platform polls the old Functions drain endpoint
before sending SIGTERM. The wait is bounded at 330 seconds; hard shutdown
limits remain. Older Functions releases without the endpoint receive that
bounded grace rather than immediate termination. Fixed-port apps still require
exclusive activation and are not advertised as continuously serving.

This release publishes only the Functions app. The platform change remains
unreleased. Functions 1.11.2 independently fixes startup recovery; on the
existing platform, the installation can still be unavailable while upgrading.
A future platform release is needed to preserve old-instance routing and
coordinate draining.
No production installation was modified during local validation.

## Local validation

Synthetic SQLite fixture: 262,144 historical invocations with 32 KiB payloads;
8,761,782,272 bytes on disk. It uses the app schema and SQLite driver, with
migration 005 already applied, matching the reported upgrade condition.
No production data or AI calls were used.

| Operation | Local duration |
| --- | ---: |
| Old status-filter recovery query | 7.466 s |
| Migration 007 on the populated database | 5.023 ms |
| New complete pool initialization | 2.971 ms |
| New migration plus pool initialization | 7.994 ms |

This demonstrates removal of the history-dependent startup scan. It does not
predict total production startup time, disk-cache conditions, or the time to
prepare all active functions. An earlier independent run measured 8.387 s for
the old query and 6.667 ms for new pool initialization.

Regression coverage includes process-lock release on SIGKILL, overlapping
owners, abandoned builds/invocations, completed-work preservation, restore
without old locks, indexed recovery query plans, cancellation, bounded legacy
batches, legacy rollback, and an actual 6.5-second function completing during
drain. The full Functions race suite and platform app/activation tests are
part of validation; platform tests additionally exercise binding availability,
route reloads during upgrade, rollback, progress API semantics, drain
cancellation, deadline handling, and compatibility with older Functions.

Reproduce the large-database check from this directory:

```sh
GOWORK=off RUN_FUNCTIONS_LARGE_HISTORY=1 go test -run '^TestStartupLargeHistory$' -v -count=1 -timeout 300s
```

Allow at least 10 GB of temporary disk space. The fixture is deleted afterward.
Regular tests skip that large fixture:

```sh
GOWORK=off go test -race -count=1 -timeout 300s ./...
```
