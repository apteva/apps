-- fleet 006: generic delegated provider/integration grants.

CREATE TABLE IF NOT EXISTS fleet_provider_grants (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id             TEXT NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    grant_id              TEXT NOT NULL,
    app_slug              TEXT NOT NULL,
    parent_connection_id  INTEGER NOT NULL,
    tenant_connection_id  INTEGER NOT NULL DEFAULT 0,
    tenant_install_id     INTEGER NOT NULL DEFAULT 0,
    tenant_role           TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'active',
    allowed_tools         TEXT NOT NULL DEFAULT '[]',
    allowed_domains       TEXT NOT NULL DEFAULT '[]',
    allowed_from          TEXT NOT NULL DEFAULT '[]',
    metadata              TEXT NOT NULL DEFAULT '{}',
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id, grant_id)
);

CREATE INDEX IF NOT EXISTS idx_fleet_provider_grants_tenant
    ON fleet_provider_grants(tenant_id);

CREATE INDEX IF NOT EXISTS idx_fleet_provider_grants_parent
    ON fleet_provider_grants(parent_connection_id);
