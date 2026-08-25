-- SaaS v0.9.1 — optional, durable CRM customer synchronization.

ALTER TABLE saas_customers
  ADD COLUMN crm_contact_id INTEGER;
ALTER TABLE saas_customers
  ADD COLUMN crm_sync_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE saas_customers
  ADD COLUMN crm_sync_error TEXT NOT NULL DEFAULT '';
ALTER TABLE saas_customers
  ADD COLUMN crm_sync_attempted_at TIMESTAMP;
ALTER TABLE saas_customers
  ADD COLUMN crm_synced_at TIMESTAMP;

CREATE INDEX ix_saas_customers_crm_sync
  ON saas_customers(project_id, crm_sync_status, crm_sync_attempted_at);
