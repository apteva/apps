-- fleet: initial schema. Tables prefixed "fleet_".

CREATE TABLE IF NOT EXISTS fleet_tenants (
    id              TEXT    PRIMARY KEY,           -- tnt_<26-hex>
    slug            TEXT    UNIQUE NOT NULL,        -- short label, used as dir name
    kind            TEXT    NOT NULL                -- local: fleet spawned the process
                    CHECK(kind IN ('local','remote')),
    base_url        TEXT    NOT NULL,               -- http://localhost:<port> or https://<host>
    config_dir      TEXT,                            -- APTEVA_HOME for local tenants; NULL for remote
    api_key_enc     BLOB    NOT NULL,
    owner_email     TEXT    NOT NULL,
    owner_user_id   TEXT,
    current_version TEXT,
    target_version  TEXT,
    status          TEXT    NOT NULL
                    CHECK(status IN ('starting','active','suspended','stopped','disconnected','failed','deleted')),
    last_seen_at    DATETIME,
    last_health     TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_fleet_tenants_status
    ON fleet_tenants(status, last_seen_at);

CREATE TABLE IF NOT EXISTS fleet_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id     TEXT    NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL,
    actor         TEXT,
    payload       TEXT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_fleet_events_tenant
    ON fleet_events(tenant_id, id DESC);
