-- Add 'planning' to the allowed status set. SQLite has no
-- ALTER CONSTRAINT, so the CHECK widening is done via table
-- rebuild. The new row contains everything 002 left behind.
PRAGMA foreign_keys=OFF;

CREATE TABLE tasks_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id        INTEGER NOT NULL,
    title           TEXT    NOT NULL,
    notes           TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'open'
                    CHECK(status IN (
                        'open','planning','in_progress','blocked','done','cancelled'
                    )),
    assigned_thread TEXT,
    parent_task_id  INTEGER REFERENCES tasks_new(id) ON DELETE CASCADE,
    progress        INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at    DATETIME
);

INSERT INTO tasks_new SELECT * FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX IF NOT EXISTS idx_tasks_agent_status
    ON tasks(agent_id, status, id);
CREATE INDEX IF NOT EXISTS idx_tasks_parent
    ON tasks(parent_task_id);

PRAGMA foreign_keys=ON;
