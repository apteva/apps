-- conversations v0.1: one store for chat + inbox + channel routing.
--
-- Design rules the schema encodes:
--   * Inbox items ARE messages. component_kind is a real, indexed
--     column — the queryable replacement for channel-chat's
--     LIKE '%"approval-card"%' scans over components_json.
--   * Deliveries are a ledger: a row per (message, target), pending
--     until the adapter confirms. Pending rows are redelivered on
--     mount so a crash mid-send never loses a reply.

CREATE TABLE IF NOT EXISTS conversations (
    id               TEXT     PRIMARY KEY,           -- "conv-<hex>"
    project_id       TEXT     NOT NULL,
    lead_agent_id    INTEGER  NOT NULL,
    title            TEXT     NOT NULL DEFAULT 'Chat',
    kind             TEXT     NOT NULL DEFAULT 'direct',   -- direct | room
    origin           TEXT     NOT NULL DEFAULT 'web',      -- web | app
    conversation_key TEXT     NOT NULL DEFAULT '',         -- stable grouping key; '' for user chats
    -- The per-conversation core thread ("chat-<id>", spawned lazily on
    -- first inbound message). '' = not yet spawned; delivery falls back
    -- to the agent's main thread until it exists.
    thread_id        TEXT     NOT NULL DEFAULT '',
    owner_user_id    INTEGER  NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at      DATETIME
);
-- One conversation per external identity. Partial: web conversations
-- ('' key) are unlimited.
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_key
    ON conversations(conversation_key) WHERE conversation_key != '';
CREATE INDEX IF NOT EXISTS idx_conversations_project
    ON conversations(project_id);

CREATE TABLE IF NOT EXISTS messages (
    id                INTEGER  PRIMARY KEY AUTOINCREMENT,
    conversation_id   TEXT     NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role              TEXT     NOT NULL,                    -- user | agent | system
    content           TEXT     NOT NULL DEFAULT '',
    agent_id          INTEGER  NOT NULL DEFAULT 0,
    user_id           INTEGER  NOT NULL DEFAULT 0,
    external_sender   TEXT     NOT NULL DEFAULT '',         -- reserved for future transports
    thread_id         TEXT     NOT NULL DEFAULT '',
    status            TEXT     NOT NULL DEFAULT 'final',    -- streaming | final
    component_kind    TEXT     NOT NULL DEFAULT '',         -- '' | approval | report | alert | status
    severity          TEXT     NOT NULL DEFAULT '',         -- alerts: info | warn | error
    inbox_only        INTEGER  NOT NULL DEFAULT 0,          -- reports: hidden from transcript
    components_json   TEXT     NOT NULL DEFAULT '[]',
    attachments_json  TEXT     NOT NULL DEFAULT '[]',
    metadata_json     TEXT     NOT NULL DEFAULT '{}',
    client_message_id TEXT     NOT NULL DEFAULT '',         -- idempotency key
    source_app        TEXT     NOT NULL DEFAULT '',         -- inbox_post: which sidecar raised it
    callback_tool     TEXT     NOT NULL DEFAULT '',         -- inbox_post: tool to call on action
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, id);
-- The inbox query. Partial: the overwhelmingly common plain message
-- never enters the index.
CREATE INDEX IF NOT EXISTS idx_messages_inbox
    ON messages(component_kind, id) WHERE component_kind != '';
-- Idempotent sends: same client_message_id in the same conversation is
-- the same message. Partial: '' (no key supplied) is unconstrained.
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_client_id
    ON messages(conversation_id, client_message_id) WHERE client_message_id != '';

CREATE TABLE IF NOT EXISTS participants (
    conversation_id   TEXT    NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    agent_id          INTEGER NOT NULL DEFAULT 0,
    user_id           INTEGER NOT NULL DEFAULT 0,
    external_identity TEXT    NOT NULL DEFAULT '',           -- reserved for future transports
    display_name      TEXT    NOT NULL DEFAULT '',
    -- Optional link to a CRM contact (apteva://contact/N). Linking an
    -- external sender to a contact is an explicit action, never an
    -- inference.
    contact_uri       TEXT    NOT NULL DEFAULT '',
    added_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (conversation_id, agent_id, user_id, external_identity)
);

CREATE TABLE IF NOT EXISTS deliveries (
    id           INTEGER  PRIMARY KEY AUTOINCREMENT,
    message_id   INTEGER  NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    -- Adapter-scoped address: "web:user:1", "agent:286", or an
    -- authenticated sibling-app callback.
    target       TEXT     NOT NULL,
    status       TEXT     NOT NULL DEFAULT 'pending',        -- pending | delivered | failed
    attempts     INTEGER  NOT NULL DEFAULT 0,
    last_error   TEXT     NOT NULL DEFAULT '',
    delivered_at DATETIME,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (message_id, target)
);
CREATE INDEX IF NOT EXISTS idx_deliveries_pending
    ON deliveries(status) WHERE status = 'pending';

-- Per-user read watermark, one row per (user, conversation). The
-- single source of truth — no client-side shadow copy.
CREATE TABLE IF NOT EXISTS read_marks (
    user_id         INTEGER NOT NULL,
    conversation_id TEXT    NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    last_seen_id    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, conversation_id)
);
