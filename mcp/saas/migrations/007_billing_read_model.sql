-- SaaS v0.3.2 - queryable Billing projections for SaaS account reporting.

ALTER TABLE saas_commerce_operations
  ADD COLUMN billing_projection_attempted_at TIMESTAMP;

ALTER TABLE saas_commerce_operations
  ADD COLUMN billing_projection_error TEXT NOT NULL DEFAULT '';

CREATE TABLE saas_billing_invoices (
  project_id            TEXT NOT NULL,
  invoice_id            INTEGER NOT NULL,
  account_id            TEXT NOT NULL,
  billing_customer_id   INTEGER NOT NULL,
  subscription_id       INTEGER NOT NULL,
  cycle_id              INTEGER NOT NULL,
  status                TEXT NOT NULL,
  currency              TEXT NOT NULL DEFAULT '',
  total_cents           INTEGER NOT NULL DEFAULT 0,
  amount_paid_cents     INTEGER NOT NULL DEFAULT 0,
  paid_at               TIMESTAMP,
  source_created_at     TIMESTAMP,
  source_updated_at     TIMESTAMP,
  synced_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, invoice_id)
);

CREATE INDEX ix_saas_billing_invoice_account
  ON saas_billing_invoices(project_id, account_id, status, paid_at);

CREATE TABLE saas_billing_payments (
  project_id            TEXT NOT NULL,
  payment_id            INTEGER NOT NULL,
  invoice_id            INTEGER NOT NULL,
  account_id            TEXT NOT NULL,
  billing_customer_id   INTEGER NOT NULL,
  amount_cents          INTEGER NOT NULL,
  currency              TEXT NOT NULL DEFAULT '',
  method                TEXT NOT NULL DEFAULT '',
  received_at           TIMESTAMP NOT NULL,
  source_created_at     TIMESTAMP,
  synced_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, payment_id)
);

CREATE INDEX ix_saas_billing_payment_account_date
  ON saas_billing_payments(project_id, account_id, received_at);

CREATE INDEX ix_saas_billing_payment_customer_date
  ON saas_billing_payments(project_id, billing_customer_id, received_at);
