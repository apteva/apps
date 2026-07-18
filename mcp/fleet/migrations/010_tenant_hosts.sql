-- fleet 010: exact hostnames routed to managed tenants.
--
-- Domain grants own DNS delegation. Individual hostnames are persisted
-- separately so Fleet can register exact server-native ingress routes and
-- refresh their tunnel targets after a hosted tenant reconnects.

CREATE TABLE IF NOT EXISTS fleet_tenant_hosts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id       TEXT NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    hostname        TEXT NOT NULL,
    domain_grant_id INTEGER REFERENCES fleet_domain_grants(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    last_error      TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(hostname),
    UNIQUE(tenant_id, hostname)
);

CREATE INDEX IF NOT EXISTS idx_fleet_tenant_hosts_tenant
    ON fleet_tenant_hosts(tenant_id);

CREATE INDEX IF NOT EXISTS idx_fleet_tenant_hosts_grant
    ON fleet_tenant_hosts(domain_grant_id);
