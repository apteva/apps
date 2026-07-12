-- SaaS v0.2.1 - durable subscription-cycle billing orchestration.

CREATE TABLE saas_commerce_operations (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  operation_key         TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  subscription_id       INTEGER NOT NULL,
  cycle_id              INTEGER NOT NULL,
  billing_customer_id   INTEGER,
  invoice_id            INTEGER,
  status                TEXT NOT NULL DEFAULT 'pending',
  stage                 TEXT NOT NULL DEFAULT 'pending',
  attempt_count         INTEGER NOT NULL DEFAULT 0,
  prepared_json         TEXT NOT NULL DEFAULT '{}',
  payment_link_json     TEXT NOT NULL DEFAULT '{}',
  last_error            TEXT NOT NULL DEFAULT '',
  lease_until           TIMESTAMP,
  started_at            TIMESTAMP,
  completed_at          TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, operation_key)
);

CREATE UNIQUE INDEX ux_saas_commerce_invoice
  ON saas_commerce_operations(project_id, invoice_id)
  WHERE invoice_id IS NOT NULL;

CREATE INDEX ix_saas_commerce_subscription_cycle
  ON saas_commerce_operations(project_id, subscription_id, cycle_id);
