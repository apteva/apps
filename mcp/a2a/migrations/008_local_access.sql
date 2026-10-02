-- v0.6.4: target-side local communication policy.
-- A missing rule preserves the v0.6.x compatibility default (attached agents
-- in the same project may communicate). An explicit wildcard deny closes a
-- target; explicit subject rules create a selected allowlist.
CREATE TABLE IF NOT EXISTS a2a_local_access (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT    NOT NULL,
    target_agent_id  INTEGER NOT NULL,
    subject_agent_id INTEGER NOT NULL,
    action           TEXT    NOT NULL CHECK(action IN ('discover','invoke','message','continue')),
    effect           TEXT    NOT NULL CHECK(effect IN ('allow','deny')),
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    UNIQUE(project_id, target_agent_id, subject_agent_id, action)
);

CREATE INDEX IF NOT EXISTS idx_a2a_local_access_target
    ON a2a_local_access(project_id, target_agent_id, action);
CREATE INDEX IF NOT EXISTS idx_a2a_local_access_subject
    ON a2a_local_access(project_id, subject_agent_id, action);
