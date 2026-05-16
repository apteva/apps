-- Rename instance_id → agent_id to match the platform terminology
-- adopted across the other apps. The column-level rename uses
-- SQLite's ALTER TABLE RENAME COLUMN (3.25.0+), which rewrites
-- references inside existing indexes automatically; we still drop +
-- recreate the compound index so its name doesn't lie about its keys.

ALTER TABLE tasks RENAME COLUMN instance_id TO agent_id;

DROP INDEX IF EXISTS idx_tasks_instance_status;
CREATE INDEX IF NOT EXISTS idx_tasks_agent_status
    ON tasks(agent_id, status, id);
