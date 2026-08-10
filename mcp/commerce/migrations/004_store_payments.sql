ALTER TABLE commerce_stores
  ADD COLUMN payment_provider TEXT NOT NULL DEFAULT 'manual';

ALTER TABLE commerce_stores
  ADD COLUMN payment_presentation TEXT NOT NULL DEFAULT 'elements';
