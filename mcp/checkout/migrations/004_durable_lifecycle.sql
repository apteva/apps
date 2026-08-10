-- Checkout v0.3.0 - durable multi-step lifecycle and recovery.

ALTER TABLE checkout_sessions
  ADD COLUMN current_step TEXT NOT NULL DEFAULT 'information';

ALTER TABLE checkout_sessions
  ADD COLUMN buyer_details_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE checkout_sessions
  ADD COLUMN selected_shipping_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE checkout_sessions
  ADD COLUMN recovery_token_hash TEXT;

ALTER TABLE checkout_sessions
  ADD COLUMN last_validated_at TIMESTAMP;

ALTER TABLE checkout_sessions
  ADD COLUMN abandoned_at TIMESTAMP;

CREATE UNIQUE INDEX ux_checkout_recovery_token
  ON checkout_sessions(project_id, recovery_token_hash)
  WHERE recovery_token_hash IS NOT NULL;

CREATE INDEX ix_checkout_expiration
  ON checkout_sessions(project_id, status, expires_at)
  WHERE status IN ('started', 'awaiting_payment');
