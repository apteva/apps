-- a2a v0.3.0: Agent Cards, remote discovery cache, and protocol correlation.

CREATE TABLE IF NOT EXISTS a2a_node (
    singleton_id INTEGER PRIMARY KEY CHECK(singleton_id = 1),
    node_id      TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS a2a_agent_profiles (
    project_id      TEXT    NOT NULL,
    local_agent_id  INTEGER NOT NULL,
    card_id         TEXT    NOT NULL UNIQUE,
    description     TEXT    NOT NULL DEFAULT '',
    skills_json     TEXT    NOT NULL DEFAULT '[]',
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    PRIMARY KEY (project_id, local_agent_id)
);

CREATE TABLE IF NOT EXISTS a2a_remote_agents (
    ref             TEXT PRIMARY KEY,
    peer_id         TEXT NOT NULL,
    card_id         TEXT NOT NULL,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    endpoint_url    TEXT NOT NULL DEFAULT '',
    skills_json     TEXT NOT NULL DEFAULT '[]',
    card_json       TEXT NOT NULL DEFAULT '',
    fetched_at      TEXT NOT NULL,
    expires_at      TEXT NOT NULL,
    UNIQUE(peer_id, card_id)
);

CREATE INDEX IF NOT EXISTS idx_a2a_remote_agents_peer
    ON a2a_remote_agents(peer_id, name);

ALTER TABLE a2a_tasks ADD COLUMN direction TEXT NOT NULL DEFAULT 'local';
ALTER TABLE a2a_tasks ADD COLUMN peer_id TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN remote_card_id TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN protocol_task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN protocol_context_id TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN remote_task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN remote_context_id TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_tasks ADD COLUMN last_synced_at TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_a2a_tasks_protocol_task
    ON a2a_tasks(protocol_task_id)
    WHERE protocol_task_id <> '';

CREATE INDEX IF NOT EXISTS idx_a2a_tasks_remote_open
    ON a2a_tasks(project_id, direction, status, updated_at)
    WHERE direction = 'outbound';
