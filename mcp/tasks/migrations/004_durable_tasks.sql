-- This migration may still be reached by legacy installations. Preserve the
-- exact v2 ledger for audit/recovery; applied migrations are not rerun by SDK.
ALTER TABLE tasks RENAME TO tasks_legacy_v2;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    agent_id INTEGER NOT NULL,
    project_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('queued','running','waiting','blocked','completed','failed','cancelled')),
    progress INTEGER CHECK (progress IS NULL OR (progress >= 0 AND progress <= 100)),
    current_step TEXT NOT NULL DEFAULT '',
    created_by_thread_id TEXT NOT NULL DEFAULT '',
    assigned_thread_id TEXT NOT NULL,
    execution_thread_id TEXT NOT NULL DEFAULT '',
    parent_task_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    schedule_kind TEXT NOT NULL DEFAULT '',
    schedule_expression TEXT NOT NULL DEFAULT '',
    schedule_timezone TEXT NOT NULL DEFAULT '',
    schedule_enabled INTEGER NOT NULL DEFAULT 0,
    schedule_overlap_policy TEXT NOT NULL DEFAULT 'skip',
    schedule_catchup_policy TEXT NOT NULL DEFAULT 'skip',
    next_run_at TEXT,
    last_run_at TEXT,
    scheduled_for TEXT,
    schedule_occurrence_key TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT
);

CREATE UNIQUE INDEX idx_tasks_agent_idempotency
    ON tasks(agent_id, idempotency_key) WHERE idempotency_key <> '';
CREATE INDEX idx_tasks_project_updated ON tasks(project_id, updated_at DESC);
CREATE INDEX idx_tasks_agent_updated ON tasks(agent_id, updated_at DESC);
CREATE INDEX idx_tasks_due ON tasks(schedule_enabled, next_run_at);
CREATE UNIQUE INDEX idx_tasks_occurrence
    ON tasks(parent_task_id, schedule_occurrence_key)
    WHERE parent_task_id <> '' AND schedule_occurrence_key <> '';

CREATE TABLE task_events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    thread_id TEXT NOT NULL DEFAULT '',
    from_state TEXT NOT NULL DEFAULT '',
    to_state TEXT NOT NULL DEFAULT '',
    data_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE INDEX idx_task_events_task_created ON task_events(task_id, created_at ASC);

-- Project/thread identity is resolved from the platform before imported rows
-- become visible. Active legacy work requires reassignment, never blind replay.
INSERT INTO tasks(id,agent_id,project_id,title,description,state,progress,current_step,
 assigned_thread_id,result,error,created_at,updated_at,completed_at)
SELECT 'legacy-'||id,agent_id,'',title,notes,
 CASE status WHEN 'done' THEN 'completed' WHEN 'cancelled' THEN 'cancelled' ELSE 'blocked' END,
 CASE WHEN status='done' THEN 100 ELSE MAX(0,MIN(100,progress)) END,
 CASE WHEN status IN ('done','cancelled') THEN '' ELSE 'Imported legacy work; verify outcome and reassign before continuing' END,
 COALESCE(assigned_thread,''),CASE WHEN status='done' THEN notes ELSE '' END,'',
 strftime('%Y-%m-%dT%H:%M:%fZ',created_at),strftime('%Y-%m-%dT%H:%M:%fZ',updated_at),
 CASE WHEN completed_at IS NOT NULL THEN strftime('%Y-%m-%dT%H:%M:%fZ',completed_at) ELSE NULL END
FROM tasks_legacy_v2;
