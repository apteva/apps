-- todo v0.1: personal todo list, sibling of `tasks` (agent mission
-- board). Differences from tasks:
--   * scoped by project_id (apteva project), not by instance_id
--   * priority (1=highest..4=lowest, Todoist convention)
--   * due_at + snoozed_until for time-based filtering
--   * tags many-to-many for cross-cutting context (#home, #work…)
--   * rrule for daily-style recurrence; on completion of a recurring
--     todo, the handler rolls due_at forward instead of marking done
--   * source tracks who authored the row so the panel can show a
--     small badge on agent-created items
--
-- Times are stored as RFC3339 UTC. Dates only ('2026-05-02') are
-- allowed in due_at when the todo is "all-day" (no time component);
-- the panel renders both.

CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,                       -- apteva project scope
    name        TEXT    NOT NULL,
    color       TEXT    NOT NULL DEFAULT '#3b82f6',
    archived    INTEGER NOT NULL DEFAULT 0,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_projects_scope ON projects(project_id, archived);

CREATE TABLE IF NOT EXISTS todos (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,                   -- apteva project scope
    project_ref     INTEGER REFERENCES projects(id) ON DELETE SET NULL,
    title           TEXT    NOT NULL,
    notes           TEXT    NOT NULL DEFAULT '',
    priority        INTEGER NOT NULL DEFAULT 4
                    CHECK(priority BETWEEN 1 AND 4),
    due_at          TEXT,                               -- RFC3339 UTC or YYYY-MM-DD
    snoozed_until   TEXT,                               -- RFC3339 UTC; nullable
    rrule           TEXT    NOT NULL DEFAULT '',        -- '' = one-off
    status          TEXT    NOT NULL DEFAULT 'open'
                    CHECK(status IN ('open','done','cancelled')),
    completed_at    TEXT,
    source          TEXT    NOT NULL DEFAULT 'human'
                    CHECK(source IN ('human','agent')),
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The hot list query is "open todos in this scope, sorted by due_at".
CREATE INDEX IF NOT EXISTS idx_todos_scope_status_due ON todos(project_id, status, due_at);
CREATE INDEX IF NOT EXISTS idx_todos_project_ref     ON todos(project_ref);

CREATE TABLE IF NOT EXISTS tags (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    UNIQUE(project_id, name)
);

CREATE TABLE IF NOT EXISTS todo_tags (
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY(todo_id, tag_id)
);

CREATE INDEX IF NOT EXISTS idx_todo_tags_tag ON todo_tags(tag_id);
