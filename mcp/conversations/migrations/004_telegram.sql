-- conversations v0.9: Telegram transport through platform connections.

-- One webhook endpoint per Telegram bot connection. The Bot API token remains
-- in the platform connection; this table stores only routing metadata and the
-- independent webhook verification secret.
CREATE TABLE IF NOT EXISTS telegram_connections (
    connection_id     INTEGER  PRIMARY KEY,
    webhook_key       TEXT     NOT NULL UNIQUE,
    webhook_secret    TEXT     NOT NULL,
    webhook_url       TEXT     NOT NULL,
    bot_id            TEXT     NOT NULL DEFAULT '',
    bot_username      TEXT     NOT NULL DEFAULT '',
    created_by_user_id INTEGER NOT NULL DEFAULT 0,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Explicit chat routing is fail-closed: an update from an unbound chat is
-- acknowledged but never becomes a conversation or platform user.
CREATE TABLE IF NOT EXISTS telegram_bindings (
    id                    TEXT     PRIMARY KEY,
    connection_id         INTEGER  NOT NULL REFERENCES telegram_connections(connection_id) ON DELETE CASCADE,
    project_id            TEXT     NOT NULL,
    conversation_id       TEXT     NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    chat_id                TEXT     NOT NULL,
    allowed_user_ids_json  TEXT     NOT NULL DEFAULT '[]',
    created_by_user_id     INTEGER  NOT NULL DEFAULT 0,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (connection_id, chat_id)
);
CREATE INDEX IF NOT EXISTS idx_telegram_bindings_project
    ON telegram_bindings(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_telegram_bindings_conversation
    ON telegram_bindings(conversation_id);

-- Telegram retries webhook deliveries. Claiming update_id before processing
-- makes inbound messages and callbacks idempotent; a failed processing attempt
-- releases the claim so Telegram can retry.
CREATE TABLE IF NOT EXISTS telegram_updates (
    connection_id INTEGER  NOT NULL REFERENCES telegram_connections(connection_id) ON DELETE CASCADE,
    update_id     INTEGER  NOT NULL,
    received_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (connection_id, update_id)
);
CREATE INDEX IF NOT EXISTS idx_telegram_updates_received
    ON telegram_updates(received_at);

-- Maps the one durable Conversations row to the Telegram message created for
-- each binding. Approval resolution edits that message instead of posting a
-- second card, and successful outbox retries never duplicate it.
CREATE TABLE IF NOT EXISTS telegram_message_links (
    binding_id          TEXT     NOT NULL REFERENCES telegram_bindings(id) ON DELETE CASCADE,
    message_id          INTEGER  NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    telegram_message_id INTEGER  NOT NULL,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (binding_id, message_id),
    UNIQUE (binding_id, telegram_message_id)
);

-- Callback data contains only this random token. The actual action and message
-- stay server-side, keeping the payload short and preventing forged action ids.
CREATE TABLE IF NOT EXISTS telegram_action_tokens (
    token       TEXT     PRIMARY KEY,
    binding_id  TEXT     NOT NULL REFERENCES telegram_bindings(id) ON DELETE CASCADE,
    message_id  INTEGER  NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    action_id   TEXT     NOT NULL,
    expires_at  DATETIME NOT NULL,
    used_at     DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (binding_id, message_id, action_id)
);
CREATE INDEX IF NOT EXISTS idx_telegram_action_expiry
    ON telegram_action_tokens(expires_at);
