-- a2a v0.4.0: mutable, generic peer registry.

CREATE TABLE IF NOT EXISTS a2a_peers (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    base_url              TEXT NOT NULL,
    encrypted_token       BLOB NOT NULL,
    token_hash            BLOB NOT NULL UNIQUE,
    discover_agents_json  TEXT NOT NULL DEFAULT '[]',
    invoke_agents_json    TEXT NOT NULL DEFAULT '[]',
    owner_install_id      INTEGER,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_a2a_peers_name
    ON a2a_peers(name);
