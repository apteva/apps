# Media v0.16.0 — incremental indexing

## Problem

Every indexer run (default 30 seconds, per project) called `SearchAllFiles`,
which read every Storage page (5,000 full rows per page, deep `OFFSET`, a minted
URL per row) only to pick at most 25 files. The same run resolved every media row
against Storage for orphan checks. On a project with ~138k files that is about 28
large listing requests plus one batch lookup per 500 media rows, every run, for a
catalog that had not changed. Offset paging over `updated_at DESC` could also skip
files that changed mid-scan.

## Changes

- `index_queue`: durable, one row per (project, file); re-enqueueing keeps the
  newest op and highest revision and bumps a generation so an in-flight attempt
  cannot retire newer work.
- `index_sync_state`: per-project feed cursor, mode (`feed` / `legacy`), sweep
  cursor and timestamps, orphan cursor, and the SSE replay cursor.
- Change feed: `GET /files/changes` pages are queued and the cursor advanced in one
  transaction. `pruned_through` above the cursor restarts the sweep.
- Reconciliation sweep: keyset discovery (`after_id`, `fields=discovery`), one
  1,000-file page per run; files needing work are queued, not processed inline.
  Orphan checks run 500 media rows per run.
- Orphans are purged only after Storage answers 404 for the exact id (at most 100
  confirmations per run). Batch omissions and failures are inconclusive.
- Candidate checks read only the media rows for the files under consideration,
  not the whole table. Derivation repairs come from a direct query.
- Storage events: `file.added` and `file.updated` are queued before the immediate
  attempt; the SSE cursor is saved after each handled event.
- New setting `reconcile_interval_hours` (default 12).

## Compatibility

Storage 0.14.0+ enables the feed and keyset discovery. Older Storage is detected
(feed request rejected) and handled in legacy mode, re-checked hourly. Migration
033 adds the two tables; existing installs start with one sweep for initial
discovery.

## Verification

`GOWORK=off go test .` — new `incremental_test.go` covers: no inventory calls on
steady-state runs for a 2,500-file indexed catalog; feed queueing, cursor
persistence and replay without duplicates; tombstones; generation-guarded acks;
pruned-feed restart; legacy fallback; explicit-404 orphan rule; SSE cursor.
