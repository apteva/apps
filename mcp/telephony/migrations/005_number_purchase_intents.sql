CREATE TABLE IF NOT EXISTS number_purchase_intents (
    token                   TEXT PRIMARY KEY,
    project_id              TEXT NOT NULL,
    provider_slug           TEXT NOT NULL,
    carrier_connection_id   INTEGER NOT NULL,
    country                 TEXT NOT NULL,
    phone_number            TEXT NOT NULL,
    number_type             TEXT NOT NULL,
    monthly_price           TEXT NOT NULL DEFAULT '',
    upfront_price           TEXT NOT NULL DEFAULT '',
    inbound_price           TEXT NOT NULL DEFAULT '',
    currency                TEXT NOT NULL DEFAULT '',
    status                  TEXT NOT NULL DEFAULT 'quoted',
    response_json           TEXT NOT NULL DEFAULT '',
    error_message           TEXT NOT NULL DEFAULT '',
    expires_at              TEXT NOT NULL,
    created_at              TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_number_purchase_intents_project_status
    ON number_purchase_intents(project_id, status, expires_at);
