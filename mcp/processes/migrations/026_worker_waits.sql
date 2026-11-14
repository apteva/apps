-- Legacy waits retain their owner until that worker explicitly checkpoints.
ALTER TABLE process_step_runs ADD COLUMN wait_json TEXT NOT NULL DEFAULT '';
ALTER TABLE process_step_runs ADD COLUMN wait_wake_at TEXT NOT NULL DEFAULT '';
CREATE INDEX process_worker_wait_timers ON process_step_runs(project_id,wait_wake_at) WHERE wait_wake_at<>'';
CREATE INDEX process_worker_wait_legacy ON process_step_runs(project_id,updated_at)
 WHERE state IN ('waiting','blocked') AND retain_worker=1 AND wait_json='';
CREATE TABLE process_wait_checks (
 step_id TEXT PRIMARY KEY REFERENCES process_step_runs(id),
 revision INTEGER NOT NULL,
 event_id TEXT NOT NULL,
 checked_at TEXT NOT NULL
);
