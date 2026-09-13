ALTER TABLE process_runs ADD COLUMN workflow INTEGER NOT NULL DEFAULT 0;
CREATE TABLE process_step_runs (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 step_key TEXT NOT NULL,
 position INTEGER NOT NULL,
 definition_json TEXT NOT NULL,
 executor_json TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending',
 progress INTEGER NOT NULL DEFAULT 0,
 output TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 decision TEXT NOT NULL DEFAULT '',
 updated_by TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 task_id TEXT NOT NULL DEFAULT '',
 delivered_at TEXT NOT NULL DEFAULT '',
 target_thread_id TEXT NOT NULL DEFAULT '',
 execution_id TEXT NOT NULL DEFAULT '',
 delivery_warning TEXT NOT NULL DEFAULT '',
 delivery_attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 lifecycle_sequence INTEGER NOT NULL DEFAULT -1,
 execution_state TEXT NOT NULL DEFAULT '',
 UNIQUE(run_id,step_key)
);
CREATE INDEX step_runs_run ON process_step_runs(run_id,position);
CREATE TABLE process_step_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 step_id TEXT NOT NULL REFERENCES process_step_runs(id),
 actor TEXT NOT NULL,
 state TEXT NOT NULL,
 decision TEXT NOT NULL DEFAULT '',
 output TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
