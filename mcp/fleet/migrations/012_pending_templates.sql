-- fleet 012: preserve a requested parent-project template while a tenant is
-- being created. Auto-setup normally applies it immediately; if setup falls
-- back to setup_pending, tenant_attach_key resumes the same import/apply flow.

CREATE TABLE IF NOT EXISTS fleet_pending_templates (
    tenant_id          TEXT PRIMARY KEY REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    source_project_id  TEXT NOT NULL,
    source_template_id TEXT NOT NULL,
    template_json      TEXT NOT NULL,
    description        TEXT NOT NULL,
    created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME DEFAULT CURRENT_TIMESTAMP
);
