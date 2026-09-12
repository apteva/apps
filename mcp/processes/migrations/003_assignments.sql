CREATE TABLE process_assignments (
 id TEXT PRIMARY KEY,
 process_id TEXT NOT NULL REFERENCES processes(id),
 revision INTEGER NOT NULL DEFAULT 1,
 body_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'paused' CHECK(status IN ('active','paused','archived')),
 sync_pending INTEGER NOT NULL DEFAULT 0,
 sync_error TEXT NOT NULL DEFAULT '',
 next_run_at TEXT NOT NULL DEFAULT '',
 scheduled_version INTEGER NOT NULL DEFAULT 0,
 last_schedule_note TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX assignments_process ON process_assignments(process_id);
-- Preserve the exact existing deadline and backend. No new event is sent.
INSERT INTO process_assignments(id,process_id,body_json,status,sync_pending,sync_error,next_run_at,scheduled_version,last_schedule_note,created_at,updated_at)
 SELECT 'assignment-'||p.id,p.id,json_object('follow_latest',json('true'),'name','Default assignment','owner_agent_id',json_extract(v.body_json,'$.owner_agent_id'),'execution_mode',coalesce(json_extract(v.body_json,'$.execution_mode'),'tasks'),'schedule',json_extract(v.body_json,'$.schedule'),'procedure_version',p.current_version,'parameters',json('{}')),
 CASE WHEN p.status='archived' THEN 'archived' ELSE 'active' END,p.sync_pending,p.sync_error,p.next_run_at,p.scheduled_version,p.last_schedule_note,p.created_at,p.updated_at
 FROM processes p JOIN process_versions v ON v.process_id=p.id AND v.version=p.current_version;
ALTER TABLE process_runs ADD COLUMN assignment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE process_runs ADD COLUMN assignment_revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE process_runs ADD COLUMN assignment_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE process_runs ADD COLUMN overrides_json TEXT NOT NULL DEFAULT '{}';
UPDATE process_runs SET assignment_id='assignment-'||process_id,assignment_json=(
 SELECT json_object('follow_latest',json('true'),'name','Default assignment','owner_agent_id',json_extract(v.body_json,'$.owner_agent_id'),'execution_mode',coalesce(json_extract(v.body_json,'$.execution_mode'),'tasks'),'schedule',json_extract(v.body_json,'$.schedule'),'procedure_version',process_runs.version,'parameters',json('{}')) FROM process_versions v WHERE v.process_id=process_runs.process_id AND v.version=process_runs.version
);
CREATE INDEX assignment_runs ON process_runs(assignment_id,created_at DESC);
