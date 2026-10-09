-- Project-wide keyset history ordering. The function-specific index cannot
-- provide ID order across all functions in a project.
-- SQLite index creation holds a write lock: schedule the first application in
-- a maintenance window with all runtimes sharing this database stopped.
-- See HISTORY_PERFORMANCE.md before upgrading a populated installation.
CREATE INDEX IF NOT EXISTS ix_inv_project_id
ON function_invocations(project_id, id DESC);
