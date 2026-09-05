CREATE TABLE task_deliveries (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 agent_id INTEGER NOT NULL,
 event_type TEXT NOT NULL,
 target_thread_id TEXT NOT NULL,
 source_event_id TEXT NOT NULL UNIQUE,
 payload_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 next_attempt_at TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 lease_until TEXT,
 delivered_at TEXT,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_task_deliveries_due ON task_deliveries(next_attempt_at,project_id) WHERE delivered_at IS NULL;
CREATE INDEX idx_task_deliveries_task ON task_deliveries(task_id);
CREATE INDEX idx_tasks_parent_state ON tasks(parent_task_id,state);
CREATE INDEX idx_tasks_project_state_updated ON tasks(project_id,state,updated_at DESC,id DESC);
UPDATE tasks SET schedule_enabled=0,next_run_at=NULL WHERE state IN ('completed','failed','cancelled');
