ALTER TABLE tasks ADD COLUMN recovery_of_task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN original_occurrence_key TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN recovery_attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN operation_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_tasks_recovery_attempt
    ON tasks(recovery_of_task_id, recovery_attempt)
    WHERE recovery_of_task_id <> '' AND recovery_attempt > 0;

CREATE TABLE task_agent_executions (
    source_event_id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('execution','terminalization')),
    execution_id TEXT NOT NULL DEFAULT '',
    thread_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    sequence INTEGER NOT NULL DEFAULT 0,
    dispatched_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deadline_at TEXT
);

CREATE UNIQUE INDEX idx_task_agent_executions_execution
    ON task_agent_executions(execution_id) WHERE execution_id <> '';
CREATE INDEX idx_task_agent_executions_task
    ON task_agent_executions(task_id, dispatched_at);
CREATE INDEX idx_task_agent_executions_deadline
    ON task_agent_executions(purpose, deadline_at)
    WHERE deadline_at IS NOT NULL;

INSERT OR IGNORE INTO task_agent_executions
    (source_event_id, task_id, purpose, execution_id, thread_id, state, reason,
     sequence, dispatched_at, updated_at, deadline_at)
SELECT agent_event_source_id, id, 'execution', agent_execution_id,
       execution_thread_id, agent_execution_state, agent_execution_reason,
       agent_lifecycle_sequence, COALESCE(dispatched_at, updated_at),
       COALESCE(agent_execution_updated_at, updated_at), agent_settle_deadline_at
FROM tasks WHERE agent_event_source_id <> '';
