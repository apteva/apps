-- Hosted Stripe Checkout and native Stripe Elements share the same durable
-- session ledger. Client secrets are deliberately never persisted.

ALTER TABLE billing_checkout_sessions
  ADD COLUMN presentation TEXT NOT NULL DEFAULT 'hosted';

ALTER TABLE billing_checkout_sessions
  ADD COLUMN idempotency_key TEXT;

ALTER TABLE billing_checkout_sessions
  ADD COLUMN url TEXT;

CREATE UNIQUE INDEX ux_billing_checkout_idempotency
  ON billing_checkout_sessions(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
