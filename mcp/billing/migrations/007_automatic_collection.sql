-- Durable, idempotent invoice collection for recurring product billing.

ALTER TABLE billing_checkout_sessions
  ADD COLUMN save_payment_method INTEGER NOT NULL DEFAULT 0;

ALTER TABLE billing_checkout_sessions
  ADD COLUMN set_default_payment_method INTEGER NOT NULL DEFAULT 0;

CREATE TABLE billing_collection_attempts (
  id                         INTEGER PRIMARY KEY,
  project_id                 TEXT    NOT NULL,
  invoice_id                 INTEGER NOT NULL REFERENCES invoices(id),
  payment_method_id          INTEGER REFERENCES billing_payment_methods(id),
  provider                   TEXT    NOT NULL DEFAULT 'stripe',
  provider_payment_intent_id TEXT,
  idempotency_key            TEXT    NOT NULL,
  amount_cents               INTEGER NOT NULL,
  currency                   TEXT    NOT NULL,
  status                     TEXT    NOT NULL DEFAULT 'pending',
  error_code                 TEXT,
  error_message              TEXT,
  next_action                TEXT    NOT NULL DEFAULT '{}',
  created_at                 TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at                 TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at               TIMESTAMP
);

CREATE UNIQUE INDEX ux_billing_collection_idempotency
  ON billing_collection_attempts(project_id, idempotency_key);

CREATE UNIQUE INDEX ux_billing_collection_provider_intent
  ON billing_collection_attempts(provider, provider_payment_intent_id)
  WHERE provider_payment_intent_id IS NOT NULL;

CREATE INDEX ix_billing_collection_invoice
  ON billing_collection_attempts(project_id, invoice_id, status, created_at DESC);
