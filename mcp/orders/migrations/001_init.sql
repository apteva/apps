-- Orders v0.1.0 — physical commerce order ledger.
--
-- Checkout owns carts/sessions. Billing owns invoices/payments.
-- Orders owns durable operational state: order lines, fulfillment
-- submissions, shipments, returns, and audit events.

CREATE TABLE orders (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,

  order_number           TEXT,
  source                 TEXT    NOT NULL DEFAULT 'manual',
  source_ref             TEXT,
  source_payload         TEXT    NOT NULL DEFAULT '{}',

  checkout_session_id    INTEGER,
  cart_id                INTEGER,
  invoice_id             INTEGER,
  customer_id            INTEGER,

  customer_email         TEXT,
  customer_name          TEXT,
  shipping_address       TEXT    NOT NULL DEFAULT '{}',
  billing_address        TEXT    NOT NULL DEFAULT '{}',

  currency               TEXT    NOT NULL DEFAULT 'USD',
  subtotal_cents         INTEGER NOT NULL DEFAULT 0,
  tax_cents              INTEGER NOT NULL DEFAULT 0,
  shipping_cents         INTEGER NOT NULL DEFAULT 0,
  discount_cents         INTEGER NOT NULL DEFAULT 0,
  total_cents            INTEGER NOT NULL DEFAULT 0,

  payment_status         TEXT    NOT NULL DEFAULT 'unpaid',
  order_status           TEXT    NOT NULL DEFAULT 'draft',
  fulfillment_status     TEXT    NOT NULL DEFAULT 'unsubmitted',

  metadata               TEXT    NOT NULL DEFAULT '{}',

  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  paid_at                TIMESTAMP,
  cancelled_at           TIMESTAMP,
  fulfilled_at           TIMESTAMP,
  delivered_at           TIMESTAMP
);

CREATE UNIQUE INDEX ux_orders_number ON orders(project_id, order_number)
  WHERE order_number IS NOT NULL;
CREATE UNIQUE INDEX ux_orders_source ON orders(project_id, source, source_ref)
  WHERE source_ref IS NOT NULL AND source_ref != '';
CREATE INDEX ix_orders_status ON orders(project_id, order_status, updated_at DESC);
CREATE INDEX ix_orders_fulfillment ON orders(project_id, fulfillment_status, updated_at DESC);
CREATE INDEX ix_orders_invoice ON orders(project_id, invoice_id)
  WHERE invoice_id IS NOT NULL;

CREATE TABLE order_items (
  id                    INTEGER PRIMARY KEY,
  order_id              INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  position              INTEGER NOT NULL DEFAULT 0,

  catalog_product_id     INTEGER,
  catalog_price_id       INTEGER,
  sku                   TEXT,
  title                 TEXT    NOT NULL,
  quantity              REAL    NOT NULL DEFAULT 1,
  unit_amount_cents     INTEGER NOT NULL DEFAULT 0,
  currency              TEXT    NOT NULL DEFAULT 'USD',

  source_item_ref        TEXT,
  fulfillment_sku        TEXT,
  metadata              TEXT    NOT NULL DEFAULT '{}',

  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_order_items_order ON order_items(order_id, position);
CREATE INDEX ix_order_items_sku ON order_items(sku);
CREATE INDEX ix_order_items_catalog ON order_items(catalog_product_id, catalog_price_id);

CREATE TABLE fulfillments (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  order_id              INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,

  provider              TEXT    NOT NULL,
  provider_order_id      TEXT,
  warehouse_id          TEXT,
  service               TEXT,
  status                TEXT    NOT NULL DEFAULT 'queued',

  request_payload        TEXT    NOT NULL DEFAULT '{}',
  response_payload       TEXT    NOT NULL DEFAULT '{}',
  error                 TEXT,
  metadata              TEXT    NOT NULL DEFAULT '{}',

  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  submitted_at          TIMESTAMP,
  accepted_at           TIMESTAMP,
  cancelled_at          TIMESTAMP
);

CREATE INDEX ix_fulfillments_order ON fulfillments(order_id, updated_at DESC);
CREATE INDEX ix_fulfillments_provider ON fulfillments(project_id, provider, provider_order_id);
CREATE INDEX ix_fulfillments_status ON fulfillments(project_id, status, updated_at DESC);

CREATE TABLE shipments (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  order_id              INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  fulfillment_id         INTEGER REFERENCES fulfillments(id) ON DELETE SET NULL,

  provider              TEXT,
  provider_shipment_id   TEXT,
  carrier               TEXT,
  service               TEXT,
  tracking_number        TEXT,
  tracking_url           TEXT,
  status                TEXT    NOT NULL DEFAULT 'pending',
  raw_payload            TEXT    NOT NULL DEFAULT '{}',

  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  shipped_at            TIMESTAMP,
  delivered_at          TIMESTAMP
);

CREATE INDEX ix_shipments_order ON shipments(order_id, updated_at DESC);
CREATE INDEX ix_shipments_tracking ON shipments(tracking_number);

CREATE TABLE returns (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  order_id              INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,

  provider              TEXT,
  provider_return_id     TEXT,
  status                TEXT    NOT NULL DEFAULT 'requested',
  reason                TEXT,
  request_payload        TEXT    NOT NULL DEFAULT '{}',
  response_payload       TEXT    NOT NULL DEFAULT '{}',
  metadata              TEXT    NOT NULL DEFAULT '{}',

  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  received_at           TIMESTAMP,
  completed_at          TIMESTAMP
);

CREATE INDEX ix_returns_order ON returns(order_id, updated_at DESC);
CREATE INDEX ix_returns_status ON returns(project_id, status, updated_at DESC);

CREATE TABLE order_events (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  order_id              INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  actor                 TEXT    NOT NULL DEFAULT 'system',
  action                TEXT    NOT NULL,
  details               TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_order_events_order ON order_events(order_id, created_at DESC);
