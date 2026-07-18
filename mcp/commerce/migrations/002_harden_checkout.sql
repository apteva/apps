-- Commerce v0.2.0 - durable checkout orchestration and immutable sales.

ALTER TABLE commerce_sales ADD COLUMN shipping_address_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE commerce_sales ADD COLUMN billing_address_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE commerce_sales ADD COLUMN processing_error TEXT NOT NULL DEFAULT '';

CREATE TABLE commerce_sale_items (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  sale_id               INTEGER NOT NULL REFERENCES commerce_sales(id) ON DELETE CASCADE,
  variant_id            INTEGER,
  listing_id            INTEGER,
  inventory_item_id     INTEGER,
  catalog_product_id    INTEGER,
  catalog_price_id      INTEGER,
  sku                   TEXT    NOT NULL DEFAULT '',
  title_snapshot        TEXT    NOT NULL,
  unit_amount_cents     INTEGER NOT NULL,
  currency              TEXT    NOT NULL,
  quantity              REAL    NOT NULL,
  requires_shipping     INTEGER NOT NULL DEFAULT 1,
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_commerce_sale_items_sale ON commerce_sale_items(project_id, sale_id, id);

CREATE TABLE commerce_reservation_links (
  checkout_id           INTEGER NOT NULL REFERENCES commerce_checkout_sessions(id) ON DELETE CASCADE,
  reservation_id        INTEGER NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'active',
  last_error            TEXT    NOT NULL DEFAULT '',
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(checkout_id, reservation_id)
);

-- Reconcile duplicate rows created by pre-0.2 retries before enforcing the
-- idempotency keys. Preserve sale history while merging checkout sessions.
UPDATE commerce_checkout_sessions
SET checkout_session_id = NULL
WHERE checkout_session_id IS NOT NULL
  AND id NOT IN (
    SELECT MIN(id)
    FROM commerce_checkout_sessions
    WHERE checkout_session_id IS NOT NULL
    GROUP BY project_id, checkout_session_id
  );

UPDATE commerce_sales
SET checkout_id = (
  SELECT MIN(canonical.id)
  FROM commerce_checkout_sessions duplicate
  JOIN commerce_checkout_sessions canonical
    ON canonical.project_id = duplicate.project_id
   AND canonical.cart_id = duplicate.cart_id
  WHERE duplicate.id = commerce_sales.checkout_id
)
WHERE checkout_id IN (
  SELECT id
  FROM commerce_checkout_sessions
  WHERE id NOT IN (
    SELECT MIN(id)
    FROM commerce_checkout_sessions
    GROUP BY project_id, cart_id
  )
);

DELETE FROM commerce_checkout_sessions
WHERE id NOT IN (
  SELECT MIN(id)
  FROM commerce_checkout_sessions
  GROUP BY project_id, cart_id
);

UPDATE commerce_sales
SET checkout_id = NULL
WHERE checkout_id IS NOT NULL
  AND id NOT IN (
    SELECT MIN(id)
    FROM commerce_sales
    WHERE checkout_id IS NOT NULL
    GROUP BY project_id, checkout_id
  );

UPDATE commerce_sales
SET invoice_id = NULL
WHERE invoice_id IS NOT NULL
  AND id NOT IN (
    SELECT MIN(id)
    FROM commerce_sales
    WHERE invoice_id IS NOT NULL
    GROUP BY project_id, invoice_id
  );

CREATE UNIQUE INDEX ux_commerce_checkout_cart ON commerce_checkout_sessions(project_id, cart_id);
CREATE UNIQUE INDEX ux_commerce_checkout_external ON commerce_checkout_sessions(project_id, checkout_session_id)
  WHERE checkout_session_id IS NOT NULL;
CREATE UNIQUE INDEX ux_commerce_sale_checkout ON commerce_sales(project_id, checkout_id)
  WHERE checkout_id IS NOT NULL;
CREATE UNIQUE INDEX ux_commerce_sale_invoice ON commerce_sales(project_id, invoice_id)
  WHERE invoice_id IS NOT NULL;
