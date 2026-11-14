-- Project-wide telemetry can hit the platform's query budget on busy projects.
-- Observe each known cleanup owner separately, with independent page cursors.
CREATE TABLE process_worker_start_agent_scans (
 project_id TEXT NOT NULL,
 agent_id INTEGER NOT NULL,
 since_at TEXT NOT NULL,
 until_at TEXT NOT NULL DEFAULT '',
 cursor TEXT NOT NULL DEFAULT '',
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(project_id,agent_id)
);
CREATE INDEX process_worker_start_agent_scans_due ON process_worker_start_agent_scans(project_id,next_attempt_at);
INSERT INTO process_worker_start_agent_scans(project_id,agent_id,since_at)
 SELECT DISTINCT p.project_id,c.agent_id,strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 day')
 FROM process_worker_cleanup c JOIN process_runs r ON r.id=c.run_id JOIN processes p ON p.id=r.process_id;
