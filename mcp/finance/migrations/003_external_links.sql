-- finance v0.3: generic external object mapping for open banking and
-- future imports. Keep provider credentials in platform connections;
-- Finance stores only stable object IDs and small metadata needed for
-- idempotent sync.

CREATE TABLE IF NOT EXISTS external_links (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    provider      TEXT    NOT NULL,
    connection_id TEXT    NOT NULL,
    external_type TEXT    NOT NULL CHECK(external_type IN ('account','transaction')),
    external_id   TEXT    NOT NULL,
    finance_type  TEXT    NOT NULL CHECK(finance_type IN ('account','transaction')),
    finance_id    INTEGER NOT NULL,
    metadata_json TEXT    NOT NULL DEFAULT '{}',
    last_seen_at  TEXT,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, provider, connection_id, external_type, external_id)
);

CREATE INDEX IF NOT EXISTS idx_external_links_finance
    ON external_links(project_id, finance_type, finance_id);

CREATE INDEX IF NOT EXISTS idx_external_links_provider
    ON external_links(project_id, provider, connection_id, external_type);
