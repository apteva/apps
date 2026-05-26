-- fleet 005: tenant domain grants.
--
-- tenant_attach_domain stores the single public hostname that opens the
-- tenant dashboard. Domain grants are different: they delegate a base
-- domain to the tenant so tenant-local apps (Deploy, Messaging, etc.)
-- can use names under it through a Domains facade.

CREATE TABLE IF NOT EXISTS fleet_domain_grants (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id          TEXT NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    domain             TEXT NOT NULL,
    wildcard           INTEGER NOT NULL DEFAULT 1,
    status             TEXT NOT NULL DEFAULT 'active',
    domain_record_id   TEXT,
    wildcard_record_id TEXT,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id, domain)
);

CREATE INDEX IF NOT EXISTS idx_fleet_domain_grants_tenant
    ON fleet_domain_grants(tenant_id);

CREATE INDEX IF NOT EXISTS idx_fleet_domain_grants_domain
    ON fleet_domain_grants(domain);
