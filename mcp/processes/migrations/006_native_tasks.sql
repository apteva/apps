CREATE TABLE process_work_items (
 id TEXT PRIMARY KEY,
 run_id TEXT REFERENCES process_runs(id),
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
 project_id TEXT NOT NULL,
 origin TEXT NOT NULL DEFAULT 'process_step',
 required INTEGER NOT NULL DEFAULT 1,
 due_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 created_by TEXT NOT NULL DEFAULT 'workflow',
 revision INTEGER NOT NULL DEFAULT 1,
 request_key TEXT NOT NULL DEFAULT '',
 request_json TEXT NOT NULL DEFAULT '',
 UNIQUE(run_id,step_key)
);
INSERT INTO process_work_items(id,run_id,step_key,position,definition_json,executor_json,state,progress,output,error,decision,updated_by,updated_at,task_id,delivered_at,target_thread_id,execution_id,delivery_warning,delivery_attempts,next_attempt_at,lifecycle_sequence,execution_state,project_id,created_at) SELECT s.id,s.run_id,s.step_key,s.position,s.definition_json,s.executor_json,s.state,s.progress,s.output,s.error,s.decision,s.updated_by,s.updated_at,s.task_id,s.delivered_at,s.target_thread_id,s.execution_id,s.delivery_warning,s.delivery_attempts,s.next_attempt_at,s.lifecycle_sequence,s.execution_state,p.project_id,r.created_at FROM process_step_runs s JOIN process_runs r ON r.id=s.run_id JOIN processes p ON p.id=r.process_id;
CREATE TABLE process_work_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 step_id TEXT NOT NULL REFERENCES process_work_items(id),
 actor TEXT NOT NULL,
 state TEXT NOT NULL,
 decision TEXT NOT NULL DEFAULT '',
 output TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 details_json TEXT NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL
);
INSERT INTO process_work_events(id,step_id,actor,state,decision,output,error,created_at) SELECT id,step_id,actor,state,decision,output,error,created_at FROM process_step_events;
DROP TABLE process_step_events;
DROP TABLE process_step_runs;
ALTER TABLE process_work_items RENAME TO process_step_runs;
ALTER TABLE process_work_events RENAME TO process_step_events;
CREATE INDEX step_runs_run ON process_step_runs(run_id,position);
CREATE INDEX process_tasks_project ON process_step_runs(project_id,state,due_at,id);
CREATE UNIQUE INDEX process_task_request ON process_step_runs(project_id,created_by,request_key) WHERE request_key<>'';
