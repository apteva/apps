-- SaaS v0.11.0 -- durable account-level credit-pack checkout linkage.
CREATE TABLE saas_credit_checkouts (
  id                    TEXT PRIMARY KEY,
  project_id            TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  idempotency_key       TEXT NOT NULL,
  catalog_price_id      INTEGER NOT NULL,
  feature_key           TEXT NOT NULL,
  credit_units          INTEGER NOT NULL,
  quantity              INTEGER NOT NULL,
  amount_cents          INTEGER NOT NULL DEFAULT 0,
  checkout_session_id   INTEGER,
  billing_invoice_id    INTEGER,
  status                TEXT NOT NULL DEFAULT 'pending',
  grant_transaction_id  INTEGER,
  last_error            TEXT NOT NULL DEFAULT '',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at          TIMESTAMP
);
CREATE UNIQUE INDEX ux_saas_credit_checkout_idempotency ON saas_credit_checkouts(project_id, idempotency_key);
CREATE UNIQUE INDEX ux_saas_credit_checkout_invoice ON saas_credit_checkouts(project_id, billing_invoice_id) WHERE billing_invoice_id IS NOT NULL;
CREATE INDEX ix_saas_credit_checkout_status ON saas_credit_checkouts(project_id, status, updated_at);

CREATE TABLE saas_credit_refunds (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  credit_checkout_id    TEXT NOT NULL,
  invoice_id            INTEGER NOT NULL,
  payment_id            INTEGER NOT NULL,
  refund_cents          INTEGER NOT NULL,
  credit_units          INTEGER NOT NULL,
  idempotency_key       TEXT NOT NULL,
  transaction_id        INTEGER,
  status                TEXT NOT NULL DEFAULT 'pending',
  last_error            TEXT NOT NULL DEFAULT '',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_saas_credit_refund_key ON saas_credit_refunds(project_id, idempotency_key);
