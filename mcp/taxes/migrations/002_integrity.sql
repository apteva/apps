ALTER TABLE tax_obligations ADD COLUMN direction TEXT NOT NULL DEFAULT 'payable';

UPDATE tax_obligations
SET direction = 'receivable', amount_cents = ABS(amount_cents)
WHERE amount_cents < 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tax_payments_bills_payment
ON tax_payments(project_id, bills_payment_id)
WHERE bills_payment_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tax_obligations_period
ON tax_obligations(project_id, period_id, tax_type);
