-- channels: standalone chat channel owned by the Channels app.
-- The app does not join platform tables directly; agent/project/user ids
-- are copied identifiers used for filtering and platform API calls.

CREATE TABLE IF NOT EXISTS channels_chats (
    id           TEXT    PRIMARY KEY,
    agent_id     INTEGER NOT NULL,
    project_id   TEXT    NOT NULL DEFAULT '',
    title        TEXT    NOT NULL DEFAULT 'Chat',
    channel      TEXT    NOT NULL DEFAULT 'chat',
    thread_id    TEXT    NOT NULL DEFAULT '',
    last_seen_id INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_channels_chats_agent
    ON channels_chats(agent_id);

CREATE INDEX IF NOT EXISTS idx_channels_chats_project
    ON channels_chats(project_id);

CREATE TABLE IF NOT EXISTS channels_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id         TEXT    NOT NULL REFERENCES channels_chats(id) ON DELETE CASCADE,
    role            TEXT    NOT NULL CHECK(role IN ('user','agent','system')),
    content         TEXT    NOT NULL,
    user_id         INTEGER,
    thread_id       TEXT,
    status          TEXT    NOT NULL DEFAULT 'final' CHECK(status IN ('streaming','final')),
    components_json TEXT    NOT NULL DEFAULT '[]',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_channels_messages_chat_id
    ON channels_messages(chat_id, id);
