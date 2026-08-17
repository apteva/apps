-- a2a v0.1: agent-to-agent task ledger.
--
-- Statuses deliberately mirror the A2A protocol task lifecycle
-- (submitted → working → input_required → completed/failed/canceled)
-- so the same rows can back real cross-server A2A later without a
-- schema migration. kind=message is a one-way notify (created already
-- completed); kind=ask expects a reply and stays open until the
-- responder resolves it.

CREATE TABLE IF NOT EXISTS a2a_tasks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK(kind IN ('message','ask')),
    status          TEXT    NOT NULL CHECK(status IN
                    ('submitted','working','input_required','completed','failed','canceled')),
    from_agent_id   INTEGER NOT NULL,
    from_agent_name TEXT    NOT NULL DEFAULT '',
    from_thread_id  TEXT    NOT NULL DEFAULT '',
    to_agent_id     INTEGER NOT NULL,
    to_agent_name   TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_a2a_tasks_to
    ON a2a_tasks(project_id, to_agent_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_a2a_tasks_from
    ON a2a_tasks(project_id, from_agent_id, status, updated_at DESC);

-- One row per delivered message. from/to are denormalized so the
-- per-pair rate limit is a single indexed count, no join.
CREATE TABLE IF NOT EXISTS a2a_messages (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id       INTEGER NOT NULL REFERENCES a2a_tasks(id),
    from_agent_id INTEGER NOT NULL,
    to_agent_id   INTEGER NOT NULL,
    body          TEXT    NOT NULL,
    status_after  TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_a2a_messages_task
    ON a2a_messages(task_id, id);

CREATE INDEX IF NOT EXISTS idx_a2a_messages_pair_time
    ON a2a_messages(from_agent_id, to_agent_id, created_at);
