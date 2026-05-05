-- v0.2: rename the user-facing bucket from "project" to "list" so it
-- stops shadowing apteva's project scope (the project_id column,
-- which keeps its name and meaning). Tags are unaffected.
--
-- Requires SQLite ≥ 3.25 for ALTER TABLE ... RENAME COLUMN
-- (modernc.org/sqlite v1.50 ships well past that).

ALTER TABLE projects RENAME TO lists;
ALTER TABLE todos    RENAME COLUMN project_ref TO list_id;

DROP   INDEX IF EXISTS idx_projects_scope;
DROP   INDEX IF EXISTS idx_todos_project_ref;
CREATE INDEX IF NOT EXISTS idx_lists_scope   ON lists(project_id, archived);
CREATE INDEX IF NOT EXISTS idx_todos_list_id ON todos(list_id);
