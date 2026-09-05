ALTER TABLE tasks ADD COLUMN agent_event_source_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN agent_execution_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN agent_execution_state TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN agent_execution_updated_at TEXT;
ALTER TABLE tasks ADD COLUMN agent_execution_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN agent_settle_deadline_at TEXT;
ALTER TABLE tasks ADD COLUMN agent_lifecycle_sequence INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_tasks_agent_settle_deadline
    ON tasks(agent_settle_deadline_at)
    WHERE agent_settle_deadline_at IS NOT NULL;

CREATE TABLE agent_event_lifecycle_deliveries (
    delivery_id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    execution_id TEXT NOT NULL,
    lifecycle_type TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    processed_at TEXT NOT NULL
);
