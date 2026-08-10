CREATE TABLE IF NOT EXISTS envelopes (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id         TEXT    NOT NULL UNIQUE,
    project_id        TEXT    NOT NULL,
    source_file_id    INTEGER NOT NULL,
    source_name       TEXT    NOT NULL,
    source_sha256     TEXT    NOT NULL,
    completed_file_id INTEGER,
    completed_sha256  TEXT,
    audit_file_id     INTEGER,
    title             TEXT    NOT NULL,
    sender_name       TEXT    NOT NULL DEFAULT '',
    message           TEXT    NOT NULL DEFAULT '',
    status            TEXT    NOT NULL DEFAULT 'draft'
                              CHECK(status IN ('draft','sent','completed','declined','voided','expired')),
    delivery_mode     TEXT    NOT NULL DEFAULT 'manual'
                              CHECK(delivery_mode IN ('manual','messaging')),
    expires_at        TEXT    NOT NULL,
    sent_at           TEXT,
    completed_at      TEXT,
    terminal_reason   TEXT    NOT NULL DEFAULT '',
    idempotency_key   TEXT,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_envelopes_project_idempotency
    ON envelopes(project_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE INDEX IF NOT EXISTS idx_envelopes_project_status_updated
    ON envelopes(project_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS recipients (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    envelope_id      INTEGER NOT NULL REFERENCES envelopes(id) ON DELETE CASCADE,
    project_id       TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    email            TEXT    NOT NULL DEFAULT '',
    role             TEXT    NOT NULL DEFAULT 'signer'
                             CHECK(role IN ('signer','approver')),
    signing_order    INTEGER NOT NULL,
    status           TEXT    NOT NULL DEFAULT 'pending'
                             CHECK(status IN ('pending','ready','viewed','signed','approved','declined')),
    token_hash       TEXT,
    token_expires_at TEXT,
    viewed_at        TEXT,
    completed_at     TEXT,
    declined_reason  TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    UNIQUE(envelope_id, signing_order)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_recipients_token_hash
    ON recipients(token_hash)
    WHERE token_hash IS NOT NULL AND token_hash <> '';

CREATE INDEX IF NOT EXISTS idx_recipients_envelope_order
    ON recipients(envelope_id, signing_order);

CREATE TABLE IF NOT EXISTS fields (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    envelope_id INTEGER NOT NULL REFERENCES envelopes(id) ON DELETE CASCADE,
    recipient_id INTEGER NOT NULL REFERENCES recipients(id) ON DELETE CASCADE,
    project_id  TEXT    NOT NULL,
    field_type  TEXT    NOT NULL
                        CHECK(field_type IN ('signature','initials','date_signed','text','checkbox')),
    page        INTEGER NOT NULL,
    x           REAL    NOT NULL,
    y           REAL    NOT NULL,
    width       REAL    NOT NULL,
    height      REAL    NOT NULL,
    label       TEXT    NOT NULL DEFAULT '',
    required    INTEGER NOT NULL DEFAULT 1 CHECK(required IN (0,1)),
    created_at  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fields_envelope_recipient
    ON fields(envelope_id, recipient_id, page, id);

CREATE TABLE IF NOT EXISTS field_values (
    field_id     INTEGER PRIMARY KEY REFERENCES fields(id) ON DELETE CASCADE,
    envelope_id  INTEGER NOT NULL REFERENCES envelopes(id) ON DELETE CASCADE,
    recipient_id INTEGER NOT NULL REFERENCES recipients(id) ON DELETE CASCADE,
    project_id   TEXT    NOT NULL,
    value_text   TEXT    NOT NULL DEFAULT '',
    signed_at    TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    envelope_id   INTEGER NOT NULL REFERENCES envelopes(id) ON DELETE CASCADE,
    project_id    TEXT    NOT NULL,
    recipient_id  INTEGER,
    event_type    TEXT    NOT NULL,
    detail_json   TEXT    NOT NULL DEFAULT '{}',
    occurred_at   TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_envelope_id
    ON audit_events(envelope_id, id);
