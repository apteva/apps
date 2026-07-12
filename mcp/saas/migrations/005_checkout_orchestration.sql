-- SaaS v0.3.0 - durable checkout, setup-session, and payment lifecycle state.

ALTER TABLE saas_commerce_operations
  ADD COLUMN checkout_id TEXT;

CREATE INDEX ix_saas_commerce_checkout
  ON saas_commerce_operations(project_id, checkout_id)
  WHERE checkout_id IS NOT NULL;

CREATE TABLE saas_checkouts (
  id                    TEXT PRIMARY KEY,
  project_id            TEXT NOT NULL,
  idempotency_key       TEXT NOT NULL,
  request_fingerprint   TEXT NOT NULL,
  customer_id           INTEGER NOT NULL,
  account_id            TEXT,
  plan_key              TEXT NOT NULL,
  slug                  TEXT NOT NULL,
  owner_email           TEXT NOT NULL,
  subscription_id       INTEGER,
  cycle_id              INTEGER,
  billing_customer_id   INTEGER,
  invoice_id            INTEGER,
  payment_method_id     INTEGER,
  status                TEXT NOT NULL DEFAULT 'pending',
  stage                 TEXT NOT NULL DEFAULT 'pending',
  payment_mode          TEXT NOT NULL DEFAULT '',
  payment_url           TEXT NOT NULL DEFAULT '',
  provider_session_id   TEXT NOT NULL DEFAULT '',
  trial_ends_at         TIMESTAMP,
  session_expires_at    TIMESTAMP,
  attempt_count         INTEGER NOT NULL DEFAULT 0,
  result_json           TEXT NOT NULL DEFAULT '{}',
  last_error            TEXT NOT NULL DEFAULT '',
  lease_until           TIMESTAMP,
  started_at            TIMESTAMP,
  completed_at          TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, idempotency_key),
  UNIQUE(project_id, slug)
);

CREATE UNIQUE INDEX ux_saas_checkout_invoice
  ON saas_checkouts(project_id, invoice_id)
  WHERE invoice_id IS NOT NULL;

CREATE INDEX ix_saas_checkout_subscription
  ON saas_checkouts(project_id, subscription_id)
  WHERE subscription_id IS NOT NULL;

CREATE INDEX ix_saas_checkout_setup_wait
  ON saas_checkouts(project_id, status, session_expires_at);
