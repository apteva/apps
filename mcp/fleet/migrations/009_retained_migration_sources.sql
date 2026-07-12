CREATE TABLE IF NOT EXISTS fleet_retained_sources (
    tenant_id          TEXT PRIMARY KEY REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    source_instance_id INTEGER NOT NULL,
    source_config_dir  TEXT NOT NULL,
    source_slug        TEXT NOT NULL,
    created_at         DATETIME NOT NULL
);
