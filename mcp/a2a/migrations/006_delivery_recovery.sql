-- Persist results and delivery intent together, independently of task lifecycle.
CREATE TABLE a2a_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES a2a_tasks(id),
    project_id TEXT NOT NULL,
    to_agent_id INTEGER NOT NULL,
    event TEXT NOT NULL,
    delivered INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_a2a_deliveries_pending ON a2a_deliveries(project_id, delivered, last_attempt_at, id);
ALTER TABLE a2a_tasks ADD COLUMN artifacts_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE a2a_tasks ADD COLUMN next_poll_at TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN poll_failures INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_a2a_tasks_poll ON a2a_tasks(project_id, direction, last_synced_at, id);
