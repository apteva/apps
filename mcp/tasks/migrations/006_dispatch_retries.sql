ALTER TABLE tasks ADD COLUMN dispatch_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN last_dispatch_attempt_at TEXT;

CREATE INDEX idx_tasks_unaccepted_retry
    ON tasks(state, last_dispatch_attempt_at)
    WHERE scheduled_for IS NOT NULL AND dispatched_at IS NOT NULL AND accepted_at IS NULL;
