-- Drafts are durable definitions. They deliberately have no execution
-- thread, schedule, or dispatch record until a user starts them.
PRAGMA foreign_keys=OFF;

CREATE TABLE tasks_new (
    id TEXT PRIMARY KEY,
    agent_id INTEGER NOT NULL DEFAULT 0,
    project_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    expected_outcome TEXT NOT NULL DEFAULT '',
    inputs_json TEXT NOT NULL DEFAULT '[]',
    suggested_agent_id INTEGER,
    created_by_operator_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('draft','queued','running','waiting','blocked','completed','failed','cancelled')),
    progress INTEGER CHECK (progress IS NULL OR (progress >= 0 AND progress <= 100)),
    current_step TEXT NOT NULL DEFAULT '',
    created_by_thread_id TEXT NOT NULL DEFAULT '',
    assigned_thread_id TEXT NOT NULL DEFAULT '',
    execution_thread_id TEXT NOT NULL DEFAULT '',
    parent_task_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    idempotency_scope TEXT NOT NULL DEFAULT '',
    recovery_of_task_id TEXT NOT NULL DEFAULT '',
    original_occurrence_key TEXT NOT NULL DEFAULT '',
    recovery_attempt INTEGER NOT NULL DEFAULT 0,
    recovery_reason TEXT NOT NULL DEFAULT '',
    operation_key TEXT NOT NULL DEFAULT '',
    schedule_kind TEXT NOT NULL DEFAULT '',
    schedule_expression TEXT NOT NULL DEFAULT '',
    schedule_timezone TEXT NOT NULL DEFAULT '',
    schedule_enabled INTEGER NOT NULL DEFAULT 0,
    schedule_overlap_policy TEXT NOT NULL DEFAULT 'skip',
    schedule_catchup_policy TEXT NOT NULL DEFAULT 'skip',
    next_run_at TEXT,
    last_run_at TEXT,
    last_dispatched_at TEXT,
    last_occurrence_id TEXT NOT NULL DEFAULT '',
    last_occurrence_status TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    last_result_reference TEXT NOT NULL DEFAULT '',
    scheduled_for TEXT,
    schedule_occurrence_key TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT,
    dispatch_attempts INTEGER NOT NULL DEFAULT 0,
    last_dispatch_attempt_at TEXT,
    accepted_at TEXT,
    telemetry_reference TEXT NOT NULL DEFAULT '',
    agent_event_source_id TEXT NOT NULL DEFAULT '',
    agent_execution_id TEXT NOT NULL DEFAULT '',
    agent_execution_state TEXT NOT NULL DEFAULT '',
    agent_execution_updated_at TEXT,
    agent_execution_reason TEXT NOT NULL DEFAULT '',
    agent_settle_deadline_at TEXT,
    agent_lifecycle_sequence INTEGER NOT NULL DEFAULT 0,
    result TEXT NOT NULL DEFAULT '',
    result_reference TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    CHECK (state <> 'draft' OR (agent_id=0 AND assigned_thread_id='' AND execution_thread_id='' AND schedule_kind='' AND schedule_enabled=0 AND next_run_at IS NULL AND scheduled_for IS NULL AND dispatched_at IS NULL AND started_at IS NULL AND progress IS NULL))
);

INSERT INTO tasks_new (
    id, agent_id, project_id, title, description, expected_outcome,
    inputs_json, suggested_agent_id, created_by_operator_id, state, progress,
    current_step, created_by_thread_id, assigned_thread_id, execution_thread_id,
    parent_task_id, idempotency_key, idempotency_scope, recovery_of_task_id, original_occurrence_key,
    recovery_attempt, recovery_reason, operation_key, schedule_kind,
    schedule_expression, schedule_timezone, schedule_enabled,
    schedule_overlap_policy, schedule_catchup_policy, next_run_at, last_run_at,
    last_dispatched_at, last_occurrence_id, last_occurrence_status, last_error,
    last_result_reference, scheduled_for, schedule_occurrence_key, dispatched_at,
    dispatch_attempts, last_dispatch_attempt_at, accepted_at, telemetry_reference,
    agent_event_source_id, agent_execution_id, agent_execution_state,
    agent_execution_updated_at, agent_execution_reason, agent_settle_deadline_at,
    agent_lifecycle_sequence, result, result_reference, error, created_at,
    updated_at, started_at, completed_at
)
SELECT
    id, agent_id, project_id, title, description, '', '[]', NULL, '', state,
    progress, current_step, created_by_thread_id, assigned_thread_id,
    execution_thread_id, parent_task_id, idempotency_key, 'agent:'||agent_id, recovery_of_task_id,
    original_occurrence_key, recovery_attempt, recovery_reason, operation_key,
    schedule_kind, schedule_expression, schedule_timezone, schedule_enabled,
    schedule_overlap_policy, schedule_catchup_policy, next_run_at, last_run_at,
    last_dispatched_at, last_occurrence_id, last_occurrence_status, last_error,
    last_result_reference, scheduled_for, schedule_occurrence_key, dispatched_at,
    dispatch_attempts, last_dispatch_attempt_at, accepted_at, telemetry_reference,
    agent_event_source_id, agent_execution_id, agent_execution_state,
    agent_execution_updated_at, agent_execution_reason, agent_settle_deadline_at,
    agent_lifecycle_sequence, result, result_reference, error, created_at,
    updated_at, started_at, completed_at
FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

CREATE UNIQUE INDEX idx_tasks_agent_idempotency
    ON tasks(idempotency_scope, idempotency_key) WHERE idempotency_key <> '';
CREATE INDEX idx_tasks_project_updated ON tasks(project_id, updated_at DESC);
CREATE INDEX idx_tasks_agent_updated ON tasks(agent_id, updated_at DESC);
CREATE INDEX idx_tasks_due ON tasks(schedule_enabled, next_run_at);
CREATE UNIQUE INDEX idx_tasks_occurrence
    ON tasks(parent_task_id, schedule_occurrence_key)
    WHERE parent_task_id <> '' AND schedule_occurrence_key <> '';
CREATE INDEX idx_tasks_unaccepted_dispatch
    ON tasks(state, dispatched_at)
    WHERE parent_task_id <> '' AND accepted_at IS NULL;
CREATE INDEX idx_tasks_unaccepted_retry
    ON tasks(state, last_dispatch_attempt_at)
    WHERE scheduled_for IS NOT NULL AND dispatched_at IS NOT NULL AND accepted_at IS NULL;
CREATE INDEX idx_tasks_agent_settle_deadline
    ON tasks(agent_settle_deadline_at)
    WHERE agent_settle_deadline_at IS NOT NULL;
CREATE UNIQUE INDEX idx_tasks_recovery_attempt
    ON tasks(recovery_of_task_id, recovery_attempt)
    WHERE recovery_of_task_id <> '' AND recovery_attempt > 0;
CREATE INDEX idx_tasks_parent_state ON tasks(parent_task_id,state);
CREATE INDEX idx_tasks_project_state_updated ON tasks(project_id,state,updated_at DESC,id DESC);

PRAGMA foreign_keys=ON;
