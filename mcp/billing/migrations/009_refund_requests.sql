-- Billing v0.12.0 - durable merchant-initiated processor refunds.

CREATE TABLE billing_refund_requests (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  invoice_id            INTEGER NOT NULL REFERENCES invoices(id),
  payment_id            INTEGER NOT NULL REFERENCES payments(id),
  provider              TEXT    NOT NULL DEFAULT 'stripe',
  provider_payment_id   TEXT    NOT NULL,
  provider_refund_id    TEXT    NOT NULL DEFAULT '',
  idempotency_key       TEXT    NOT NULL,
  amount_cents          INTEGER NOT NULL,
  currency              TEXT    NOT NULL,
  reason                TEXT    NOT NULL DEFAULT 'requested_by_customer',
  status                TEXT    NOT NULL DEFAULT 'pending',
  error                 TEXT    NOT NULL DEFAULT '',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at          TIMESTAMP,
  UNIQUE(project_id, idempotency_key),
  CHECK (amount_cents > 0),
  CHECK (status IN ('pending', 'submitted', 'succeeded', 'failed'))
);

CREATE INDEX ix_billing_refunds_invoice
  ON billing_refund_requests(project_id, invoice_id, created_at DESC);
