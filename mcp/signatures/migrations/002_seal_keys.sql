-- Self-signed document seal, generated on first finalization when no
-- seal_certificate is configured. Single row per install.
CREATE TABLE IF NOT EXISTS seal_keys (
    id         INTEGER PRIMARY KEY CHECK(id = 1),
    key_pem    TEXT    NOT NULL,
    cert_pem   TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);
