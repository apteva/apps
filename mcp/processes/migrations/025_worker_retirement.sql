ALTER TABLE process_worker_cleanup ADD COLUMN reason TEXT NOT NULL DEFAULT 'terminal';
CREATE INDEX process_cleanup_thread ON process_worker_cleanup(agent_id,thread_id);
CREATE TABLE process_worker_slots (
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,thread_id)
);
INSERT INTO process_worker_slots
 SELECT w.agent_id,w.thread_id,w.run_id,r.created_at FROM process_owned_workers w
 JOIN process_runs r ON r.id=w.run_id
 WHERE r.state NOT IN ('completed','failed','cancelled')
 AND NOT EXISTS(SELECT 1 FROM process_worker_cleanup c WHERE c.run_id=w.run_id AND c.agent_id=w.agent_id AND c.thread_id=w.thread_id AND c.completed_at<>'')
 ON CONFLICT DO NOTHING;

-- Preserve callback ownership even when a later runtime error changes the state.
ALTER TABLE process_step_runs ADD COLUMN retain_worker INTEGER NOT NULL DEFAULT 0;
CREATE INDEX process_step_events_latest ON process_step_events(step_id,id DESC);
UPDATE process_step_runs SET retain_worker=1
 WHERE state IN ('waiting','blocked') AND (
 updated_by<>'runtime' OR COALESCE((SELECT e.state FROM process_step_events e
 WHERE e.step_id=process_step_runs.id AND e.actor<>'runtime' ORDER BY e.id DESC LIMIT 1),'') IN ('waiting','blocked'));
