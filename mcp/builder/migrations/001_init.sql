PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS builder_goals (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL,
    owner_agent_id      INTEGER NOT NULL,
    title               TEXT NOT NULL,
    objective           TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'planning'
                        CHECK (status IN ('planning','active','waiting_approval','blocked','completed','cancelled')),
    current_phase       TEXT NOT NULL DEFAULT '',
    summary             TEXT NOT NULL DEFAULT '',
    next_action         TEXT NOT NULL DEFAULT '',
    success_criteria    TEXT NOT NULL DEFAULT '[]',
    constraints_json    TEXT NOT NULL DEFAULT '[]',
    idempotency_key     TEXT NOT NULL DEFAULT '',
    created_by_thread   TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS builder_goals_idempotency
    ON builder_goals(project_id, owner_agent_id, idempotency_key)
    WHERE idempotency_key != '';
CREATE INDEX IF NOT EXISTS builder_goals_project_status
    ON builder_goals(project_id, owner_agent_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS builder_steps (
    id                  TEXT PRIMARY KEY,
    goal_id             TEXT NOT NULL REFERENCES builder_goals(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL,
    title               TEXT NOT NULL,
    detail              TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','active','waiting_approval','blocked','completed','skipped','failed')),
    approval_state      TEXT NOT NULL DEFAULT 'none'
                        CHECK (approval_state IN ('none','required','requested','approved','denied')),
    blocking_reason     TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT NOT NULL DEFAULT '',
    UNIQUE(goal_id, position)
);

CREATE INDEX IF NOT EXISTS builder_steps_goal_status
    ON builder_steps(goal_id, status, position);

CREATE TABLE IF NOT EXISTS builder_checks (
    id                  TEXT PRIMARY KEY,
    goal_id             TEXT NOT NULL REFERENCES builder_goals(id) ON DELETE CASCADE,
    check_key           TEXT NOT NULL,
    name                TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','passing','failing','blocked')),
    result              TEXT NOT NULL DEFAULT '',
    evidence_json       TEXT NOT NULL DEFAULT '{}',
    checked_at          TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE(goal_id, check_key)
);

CREATE INDEX IF NOT EXISTS builder_checks_goal_status
    ON builder_checks(goal_id, status);

CREATE TABLE IF NOT EXISTS builder_resources (
    id                  TEXT PRIMARY KEY,
    goal_id             TEXT NOT NULL REFERENCES builder_goals(id) ON DELETE CASCADE,
    resource_key        TEXT NOT NULL,
    kind                TEXT NOT NULL
                        CHECK (kind IN ('agent','app','integration','credential','connection','project_setting','other')),
    name                TEXT NOT NULL,
    external_id         TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'planned'
                        CHECK (status IN ('planned','creating','configured','ready','drifted','needs_attention','removed')),
    desired_state_json  TEXT NOT NULL DEFAULT '{}',
    observed_state_json TEXT NOT NULL DEFAULT '{}',
    note                TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    last_checked_at     TEXT NOT NULL DEFAULT '',
    UNIQUE(goal_id, resource_key)
);

CREATE INDEX IF NOT EXISTS builder_resources_goal_status
    ON builder_resources(goal_id, status, kind);

CREATE TABLE IF NOT EXISTS builder_events (
    id                  TEXT PRIMARY KEY,
    goal_id             TEXT NOT NULL REFERENCES builder_goals(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                        CHECK (kind IN ('created','plan','progress','decision','risk','approval','operator_input','status','check','resource','note')),
    title               TEXT NOT NULL,
    detail              TEXT NOT NULL DEFAULT '',
    data_json           TEXT NOT NULL DEFAULT '{}',
    actor_agent_id      INTEGER NOT NULL DEFAULT 0,
    actor_thread_id     TEXT NOT NULL DEFAULT '',
    event_key           TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS builder_events_idempotency
    ON builder_events(goal_id, event_key)
    WHERE event_key != '';
CREATE INDEX IF NOT EXISTS builder_events_goal_time
    ON builder_events(goal_id, created_at DESC);
