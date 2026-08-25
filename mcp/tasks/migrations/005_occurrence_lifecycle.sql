ALTER TABLE tasks ADD COLUMN last_dispatched_at TEXT;
ALTER TABLE tasks ADD COLUMN last_occurrence_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN last_occurrence_status TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN last_result_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN dispatched_at TEXT;
ALTER TABLE tasks ADD COLUMN accepted_at TEXT;
ALTER TABLE tasks ADD COLUMN telemetry_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN result_reference TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_tasks_unaccepted_dispatch
    ON tasks(state, dispatched_at)
    WHERE parent_task_id <> '' AND accepted_at IS NULL;
