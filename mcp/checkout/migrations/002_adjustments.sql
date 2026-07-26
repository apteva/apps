-- Checkout v0.1.5 - frozen commerce adjustments.

ALTER TABLE checkout_sessions ADD COLUMN shipping_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checkout_sessions ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checkout_sessions ADD COLUMN adjustments_json TEXT NOT NULL DEFAULT '{}';
