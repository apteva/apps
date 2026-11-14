-- A failed owner must not abort the batch or permanently occupy its first page.
-- An unavailable owner is deferred, never acknowledged or authorized for cleanup.
CREATE TABLE process_wait_retries (
 step_id TEXT PRIMARY KEY REFERENCES process_step_runs(id),
 revision INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL,
 last_error TEXT NOT NULL
);
CREATE INDEX process_wait_retries_due ON process_wait_retries(next_attempt_at);
-- Bounded, resumable metadata backfill detects workers restored after a cleanup
-- acknowledgement, without listing full worker contexts or scanning run history.
CREATE TABLE process_worker_start_scans (
 project_id TEXT PRIMARY KEY,
 since_at TEXT NOT NULL,
 until_at TEXT NOT NULL DEFAULT '',
 cursor TEXT NOT NULL DEFAULT '',
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT ''
);
