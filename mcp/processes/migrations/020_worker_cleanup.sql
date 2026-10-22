-- The run transition and cleanup intent commit together, even if the sidecar
-- exits before contacting Core. Keep ownership and receipts after shutdown.
CREATE VIEW process_owned_workers AS
 SELECT run_id,agent_id,thread_id FROM process_run_workers WHERE agent_id>0 AND thread_id<>'' AND thread_id<>'main'
 UNION
 SELECT run_id,CAST(json_extract(executor_json,'$.agent_id') AS INTEGER),target_thread_id
 FROM process_step_runs WHERE origin='process_step' AND json_extract(executor_json,'$.kind')='agent'
 AND CAST(json_extract(executor_json,'$.agent_id') AS INTEGER)>0 AND target_thread_id<>'' AND target_thread_id<>'main';
CREATE TABLE process_worker_cleanup (
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL CHECK(thread_id<>'' AND thread_id<>'main'),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 completed_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(run_id,agent_id,thread_id)
);
CREATE INDEX process_worker_cleanup_pending ON process_worker_cleanup(next_attempt_at) WHERE completed_at='';
-- A killed worker may never reach native done, so notify its coordinator
-- independently. Backfill shutdown for old runs without spamming old outcomes.
CREATE TABLE process_run_terminal_events (
 run_id TEXT PRIMARY KEY REFERENCES process_runs(id),
 target_thread_id TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 delivered_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX process_run_terminal_events_pending ON process_run_terminal_events(next_attempt_at) WHERE delivered_at='';
CREATE TRIGGER process_terminal_worker_cleanup AFTER UPDATE OF state ON process_runs
 WHEN NEW.state IN ('completed','failed','cancelled')
 BEGIN
 INSERT INTO process_worker_cleanup(run_id,agent_id,thread_id)
 SELECT run_id,agent_id,thread_id FROM process_owned_workers WHERE run_id=NEW.id
 ON CONFLICT DO NOTHING;
 INSERT INTO process_run_terminal_events(run_id) SELECT NEW.id WHERE NEW.workflow=1 ON CONFLICT DO NOTHING;
 END;
-- Recover workers abandoned by older versions, including isolated workers.
INSERT INTO process_worker_cleanup(run_id,agent_id,thread_id)
 SELECT w.run_id,w.agent_id,w.thread_id FROM process_owned_workers w JOIN process_runs r ON r.id=w.run_id
 WHERE r.state IN ('completed','failed','cancelled');
