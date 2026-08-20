-- Commerce v0.8.8 - immutable costs and additive sale profitability ledger.

ALTER TABLE commerce_sale_items ADD COLUMN unit_cost_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sale_items ADD COLUMN cost_currency TEXT NOT NULL DEFAULT '';
ALTER TABLE commerce_sale_items ADD COLUMN cost_source TEXT NOT NULL DEFAULT '';
ALTER TABLE commerce_sale_items ADD COLUMN cost_captured_at TIMESTAMP;

ALTER TABLE commerce_sales ADD COLUMN shipping_revenue_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN product_cost_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN shipping_cost_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN payment_fee_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN refunded_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN net_revenue_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN gross_profit_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN contribution_profit_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE commerce_sales ADD COLUMN profitability_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE commerce_sales ADD COLUMN financials_updated_at TIMESTAMP;

UPDATE commerce_sales SET shipping_revenue_cents = shipping_cents;

-- Financial facts are append-only and idempotent. Positive amounts represent
-- costs/refunds; signed adjustments may be positive or negative.
CREATE TABLE commerce_sale_financial_events (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  sale_id               INTEGER NOT NULL REFERENCES commerce_sales(id) ON DELETE CASCADE,
  kind                  TEXT    NOT NULL,
  amount_cents          INTEGER NOT NULL,
  currency              TEXT    NOT NULL,
  source                TEXT    NOT NULL DEFAULT 'manual',
  idempotency_key       TEXT    NOT NULL,
  external_id           TEXT    NOT NULL DEFAULT '',
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  occurred_at           TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, idempotency_key)
);

CREATE INDEX ix_commerce_sale_financial_events_sale
  ON commerce_sale_financial_events(project_id, sale_id, kind, created_at);

CREATE UNIQUE INDEX ux_commerce_sale_financial_events_external
  ON commerce_sale_financial_events(project_id, sale_id, kind, external_id)
  WHERE external_id != '';

CREATE INDEX ix_commerce_sales_profitability
  ON commerce_sales(project_id, store_id, payment_status, paid_at, id);
