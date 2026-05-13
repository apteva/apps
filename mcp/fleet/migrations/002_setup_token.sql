-- fleet 002: setup_token + setup_pending status.
--
-- SQLite can ADD COLUMN in place but cannot broaden a CHECK constraint —
-- the only way to allow a new status value is to rebuild the table.
-- We disable FKs for the rebuild so DROP TABLE doesn't cascade-delete
-- the fleet_events rows (the FK is recreated implicitly with the table).

PRAGMA foreign_keys=OFF;

CREATE TABLE fleet_tenants_new (
    id              TEXT    PRIMARY KEY,
    slug            TEXT    UNIQUE NOT NULL,
    kind            TEXT    NOT NULL
                    CHECK(kind IN ('local','remote')),
    base_url        TEXT    NOT NULL,
    config_dir      TEXT,
    api_key_enc     BLOB    NOT NULL,
    setup_token_enc BLOB,
    owner_email     TEXT    NOT NULL,
    owner_user_id   TEXT,
    current_version TEXT,
    target_version  TEXT,
    status          TEXT    NOT NULL
                    CHECK(status IN ('starting','setup_pending','active','suspended','stopped','disconnected','failed','deleted')),
    last_seen_at    DATETIME,
    last_health     TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO fleet_tenants_new
    (id, slug, kind, base_url, config_dir, api_key_enc, owner_email, owner_user_id,
     current_version, target_version, status, last_seen_at, last_health, created_at, updated_at)
SELECT
    id, slug, kind, base_url, config_dir, api_key_enc, owner_email, owner_user_id,
    current_version, target_version, status, last_seen_at, last_health, created_at, updated_at
FROM fleet_tenants;

DROP TABLE fleet_tenants;
ALTER TABLE fleet_tenants_new RENAME TO fleet_tenants;

CREATE INDEX IF NOT EXISTS idx_fleet_tenants_status
    ON fleet_tenants(status, last_seen_at);

PRAGMA foreign_keys=ON;
