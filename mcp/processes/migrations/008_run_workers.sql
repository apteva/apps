CREATE TABLE process_run_workers (
 run_id TEXT PRIMARY KEY REFERENCES process_runs(id),
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL,
 created_at TEXT NOT NULL
);
