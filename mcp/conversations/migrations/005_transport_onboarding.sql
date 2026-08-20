-- conversations v0.10: generic transport onboarding and friendly Telegram routing.

-- One intake policy owns unknown-chat behavior for a transport connection.
-- Keeping this generic makes the authorization/onboarding state reusable by
-- future adapters; provider-specific payload parsing remains in the adapter.
CREATE TABLE IF NOT EXISTS transport_intake_policies (
    transport             TEXT     NOT NULL,
    connection_id         INTEGER  NOT NULL,
    project_id            TEXT     NOT NULL,
    mode                   TEXT     NOT NULL DEFAULT 'pairing', -- pairing | public | closed
    default_agent_id       INTEGER  NOT NULL DEFAULT 0,
    default_title          TEXT     NOT NULL DEFAULT 'New conversation',
    require_group_mention  INTEGER  NOT NULL DEFAULT 1,
    created_by_user_id     INTEGER  NOT NULL DEFAULT 0,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (transport, connection_id)
);
CREATE INDEX IF NOT EXISTS idx_transport_intake_project
    ON transport_intake_policies(project_id, transport);

-- Unknown senders are represented by minimal identity metadata only. Their
-- message content is deliberately not retained or sent to an LLM before the
-- operator approves them.
CREATE TABLE IF NOT EXISTS transport_access_requests (
    id                 TEXT     PRIMARY KEY,
    transport          TEXT     NOT NULL,
    connection_id      INTEGER  NOT NULL,
    project_id         TEXT     NOT NULL,
    external_chat_id   TEXT     NOT NULL,
    external_user_id   TEXT     NOT NULL,
    chat_type          TEXT     NOT NULL DEFAULT '',
    display_name       TEXT     NOT NULL DEFAULT '',
    username           TEXT     NOT NULL DEFAULT '',
    chat_title         TEXT     NOT NULL DEFAULT '',
    pairing_code       TEXT     NOT NULL,
    state              TEXT     NOT NULL DEFAULT 'pending', -- pending | approved | dismissed | blocked
    conversation_id    TEXT     NOT NULL DEFAULT '',
    notified_at        DATETIME,
    expires_at         DATETIME NOT NULL,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (transport, connection_id, external_chat_id, external_user_id)
);
CREATE INDEX IF NOT EXISTS idx_transport_access_project_state
    ON transport_access_requests(project_id, transport, state, created_at);

-- Operator-generated deep links use opaque, one-time tokens. Only a SHA-256
-- digest is stored, so a database read cannot recover an active invite URL.
CREATE TABLE IF NOT EXISTS transport_invites (
    id                  TEXT     PRIMARY KEY,
    token_hash          TEXT     NOT NULL UNIQUE,
    transport           TEXT     NOT NULL,
    connection_id       INTEGER  NOT NULL,
    project_id          TEXT     NOT NULL,
    conversation_id     TEXT     NOT NULL DEFAULT '',
    audience            TEXT     NOT NULL DEFAULT 'operator',
    chat_type           TEXT     NOT NULL DEFAULT 'private', -- private | group
    default_agent_id    INTEGER  NOT NULL DEFAULT 0,
    created_by_user_id  INTEGER  NOT NULL DEFAULT 0,
    expires_at          DATETIME NOT NULL,
    used_at             DATETIME,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_transport_invite_expiry
    ON transport_invites(expires_at);

-- Human-readable transport metadata is presentation/routing state, not a
-- second conversation store. Existing v0.9 rows remain valid with defaults.
ALTER TABLE telegram_bindings ADD COLUMN chat_type TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_bindings ADD COLUMN chat_title TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_bindings ADD COLUMN chat_username TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_bindings ADD COLUMN require_mention INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_bindings ADD COLUMN access_mode TEXT NOT NULL DEFAULT 'manual';
