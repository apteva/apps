-- channels: standalone channel router owned by the Channels app.
-- The app does not join platform tables directly; agent/project/user ids
-- are copied identifiers used for filtering and platform API calls.

CREATE TABLE IF NOT EXISTS channels (
    id               TEXT    PRIMARY KEY,
    project_id       TEXT    NOT NULL DEFAULT '',
    type             TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    status           TEXT    NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled')),
    default_agent_id INTEGER NOT NULL DEFAULT 0,
    config_json      TEXT    NOT NULL DEFAULT '{}',
    created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_channels_project
    ON channels(project_id);

CREATE INDEX IF NOT EXISTS idx_channels_default_agent
    ON channels(default_agent_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_ntfy_topic
    ON channels(json_extract(config_json, '$.topic'))
    WHERE type = 'ntfy';

CREATE TABLE IF NOT EXISTS conversations (
    id                 TEXT    PRIMARY KEY,
    channel_id         TEXT    NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    project_id         TEXT    NOT NULL DEFAULT '',
    agent_id           INTEGER NOT NULL DEFAULT 0,
    title              TEXT    NOT NULL DEFAULT 'Conversation',
    external_thread_id TEXT    NOT NULL DEFAULT '',
    last_seen_id       INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_conversations_channel
    ON conversations(channel_id);

CREATE INDEX IF NOT EXISTS idx_conversations_project
    ON conversations(project_id);

CREATE INDEX IF NOT EXISTS idx_conversations_agent
    ON conversations(agent_id);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT    NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT    NOT NULL CHECK(role IN ('user','agent','system')),
    content         TEXT    NOT NULL,
    user_id         INTEGER,
    thread_id       TEXT,
    status          TEXT    NOT NULL DEFAULT 'final' CHECK(status IN ('streaming','final')),
    metadata_json   TEXT    NOT NULL DEFAULT '{}',
    components_json TEXT    NOT NULL DEFAULT '[]',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation
    ON messages(conversation_id, id);
