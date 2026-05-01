-- tasks: initial schema.
--
-- The table is named `tasks` (no app-name prefix). The app's DB is
-- already isolated to its own SQLite file under /data, so the prefix
-- the original schema used buys nothing and the un-prefixed name is
-- what the tool handlers were always querying.
--
-- The kanban panel queries by (instance_id, status); compound index
-- there. assigned_thread / parent_task_id / progress are reserved for
-- v0.2 sub-tasking + per-thread assignment.

CREATE TABLE IF NOT EXISTS tasks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id     INTEGER NOT NULL,
    title           TEXT    NOT NULL,
    notes           TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'open'
                    CHECK(status IN ('open','in_progress','blocked','done','cancelled')),
    assigned_thread TEXT,                            -- nullable: reserved for v0.2
    parent_task_id  INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
    progress        INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at    DATETIME
);

CREATE INDEX IF NOT EXISTS idx_tasks_instance_status
    ON tasks(instance_id, status, id);
CREATE INDEX IF NOT EXISTS idx_tasks_parent
    ON tasks(parent_task_id);
