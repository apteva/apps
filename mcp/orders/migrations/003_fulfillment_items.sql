-- Orders v0.2.4 - split fulfillment membership and shipment idempotency.

CREATE TABLE fulfillment_items (
  fulfillment_id       INTEGER NOT NULL REFERENCES fulfillments(id) ON DELETE CASCADE,
  order_item_id        INTEGER NOT NULL REFERENCES order_items(id) ON DELETE CASCADE,
  quantity             REAL    NOT NULL,
  created_at           TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(fulfillment_id, order_item_id)
);

CREATE INDEX ix_fulfillment_items_order_item ON fulfillment_items(order_item_id);

UPDATE shipments
SET provider_shipment_id = NULL
WHERE provider_shipment_id IS NOT NULL
  AND provider_shipment_id != ''
  AND id NOT IN (
    SELECT MIN(id)
    FROM shipments
    WHERE provider_shipment_id IS NOT NULL AND provider_shipment_id != ''
    GROUP BY project_id, provider, provider_shipment_id
  );

CREATE UNIQUE INDEX ux_shipments_provider_ref
  ON shipments(project_id, provider, provider_shipment_id)
  WHERE provider_shipment_id IS NOT NULL AND provider_shipment_id != '';
