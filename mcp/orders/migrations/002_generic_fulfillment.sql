-- Orders v0.1.2 — generic commerce fulfillment.
--
-- Keep physical commerce as the default, but let Orders track SaaS,
-- hosting, digital, and service fulfillment without each product app
-- inventing its own order/fulfillment ledger.

ALTER TABLE orders ADD COLUMN order_type TEXT NOT NULL DEFAULT 'physical';

ALTER TABLE order_items ADD COLUMN fulfillment_type TEXT NOT NULL DEFAULT 'warehouse_shipment';
ALTER TABLE order_items ADD COLUMN fulfillment_app TEXT NOT NULL DEFAULT 'orders';

ALTER TABLE fulfillments ADD COLUMN fulfillment_type TEXT NOT NULL DEFAULT 'warehouse_shipment';
ALTER TABLE fulfillments ADD COLUMN fulfillment_app TEXT NOT NULL DEFAULT 'orders';
ALTER TABLE fulfillments ADD COLUMN external_ref TEXT;
ALTER TABLE fulfillments ADD COLUMN idempotency_key TEXT;
ALTER TABLE fulfillments ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;

CREATE INDEX ix_orders_type ON orders(project_id, order_type, updated_at DESC);
CREATE INDEX ix_order_items_fulfillment ON order_items(fulfillment_app, fulfillment_type);
CREATE INDEX ix_fulfillments_app ON fulfillments(project_id, fulfillment_app, fulfillment_type, status);
CREATE UNIQUE INDEX ux_fulfillments_idempotency
  ON fulfillments(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
