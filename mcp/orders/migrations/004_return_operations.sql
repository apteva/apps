-- Orders v0.3.0 - partial returns, exchanges, restocking, and refund coordination.

ALTER TABLE returns ADD COLUMN idempotency_key TEXT;
ALTER TABLE returns ADD COLUMN refund_request_id INTEGER;
ALTER TABLE returns ADD COLUMN refund_amount_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE returns ADD COLUMN exchange_order_id INTEGER;
ALTER TABLE returns ADD COLUMN restock_location_id INTEGER;
ALTER TABLE returns ADD COLUMN processing_error TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX ux_returns_idempotency
  ON returns(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';

CREATE TABLE return_items (
  return_id             INTEGER NOT NULL REFERENCES returns(id) ON DELETE CASCADE,
  order_item_id         INTEGER NOT NULL REFERENCES order_items(id),
  quantity              REAL    NOT NULL,
  inventory_item_id     INTEGER,
  restocked_at          TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(return_id, order_item_id),
  CHECK(quantity > 0)
);

CREATE INDEX ix_return_items_order_item ON return_items(order_item_id);
