-- Preserve existing run workers and allow one durable owner per executor.
ALTER TABLE process_run_workers RENAME TO process_run_workers_legacy;
CREATE TABLE process_run_workers (
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY (run_id, agent_id)
);
INSERT INTO process_run_workers SELECT run_id,agent_id,thread_id,created_at FROM process_run_workers_legacy;
DROP TABLE process_run_workers_legacy;
