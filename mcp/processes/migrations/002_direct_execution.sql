-- Existing definitions without execution_mode and existing dispatches remain
-- Tasks-backed. Never reinterpret an in-flight v0.1 run as direct execution.
ALTER TABLE processes ADD COLUMN next_run_at TEXT NOT NULL DEFAULT '';
ALTER TABLE processes ADD COLUMN scheduled_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE processes ADD COLUMN last_schedule_note TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN backend TEXT NOT NULL DEFAULT 'tasks' CHECK(backend IN ('agent','tasks'));
ALTER TABLE process_runs ADD COLUMN state TEXT NOT NULL DEFAULT 'queued';
ALTER TABLE process_runs ADD COLUMN progress INTEGER NOT NULL DEFAULT 0;
ALTER TABLE process_runs ADD COLUMN current_step TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN result TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN error TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN execution_id TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN target_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN delivered_at TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN delivery_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE process_runs ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN scheduled_for TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN schedule_paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE process_runs ADD COLUMN lifecycle_sequence INTEGER NOT NULL DEFAULT -1;
ALTER TABLE process_runs ADD COLUMN execution_state TEXT NOT NULL DEFAULT '';
CREATE INDEX process_direct_pending ON process_runs(backend, delivered_at, next_attempt_at);
