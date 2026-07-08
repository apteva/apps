-- Commerce v0.1.0 — multi-store merchandising + sale orchestration.

CREATE TABLE commerce_stores (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  slug                  TEXT    NOT NULL,
  name                  TEXT    NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'active',
  public_base_url        TEXT    NOT NULL DEFAULT '',
  default_currency       TEXT    NOT NULL DEFAULT 'USD',
  default_locale         TEXT    NOT NULL DEFAULT 'en',
  timezone              TEXT    NOT NULL DEFAULT 'UTC',
  order_number_format    TEXT    NOT NULL DEFAULT 'ORD-{yyyy}-{seq:04}',
  checkout_mode          TEXT    NOT NULL DEFAULT 'checkout_app',
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at           TIMESTAMP,
  UNIQUE(project_id, slug)
);

CREATE INDEX ix_commerce_stores_status ON commerce_stores(project_id, status);

CREATE TABLE commerce_listings (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL REFERENCES commerce_stores(id) ON DELETE CASCADE,
  catalog_product_id     INTEGER,
  handle                TEXT    NOT NULL,
  title                 TEXT    NOT NULL,
  description_html       TEXT    NOT NULL DEFAULT '',
  vendor                TEXT    NOT NULL DEFAULT '',
  product_type           TEXT    NOT NULL DEFAULT '',
  status                TEXT    NOT NULL DEFAULT 'draft',
  seo_title             TEXT    NOT NULL DEFAULT '',
  seo_description        TEXT    NOT NULL DEFAULT '',
  featured_media_id      INTEGER,
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at           TIMESTAMP,
  UNIQUE(project_id, store_id, handle)
);

CREATE INDEX ix_commerce_listings_store ON commerce_listings(project_id, store_id, status, updated_at DESC);
CREATE INDEX ix_commerce_listings_catalog ON commerce_listings(project_id, catalog_product_id);

CREATE TABLE commerce_variants (
  id                      INTEGER PRIMARY KEY,
  project_id              TEXT    NOT NULL,
  store_id                INTEGER NOT NULL,
  listing_id              INTEGER NOT NULL REFERENCES commerce_listings(id) ON DELETE CASCADE,
  catalog_price_id         INTEGER,
  inventory_item_id        INTEGER,
  sku                     TEXT    NOT NULL DEFAULT '',
  title                   TEXT    NOT NULL DEFAULT '',
  option1                 TEXT    NOT NULL DEFAULT '',
  option2                 TEXT    NOT NULL DEFAULT '',
  option3                 TEXT    NOT NULL DEFAULT '',
  price_cents             INTEGER NOT NULL DEFAULT 0,
  compare_at_price_cents   INTEGER NOT NULL DEFAULT 0,
  currency                TEXT    NOT NULL DEFAULT 'USD',
  taxable                 INTEGER NOT NULL DEFAULT 1,
  requires_shipping       INTEGER NOT NULL DEFAULT 1,
  sort_order              INTEGER NOT NULL DEFAULT 0,
  metadata_json           TEXT    NOT NULL DEFAULT '{}',
  created_at              TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at              TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_commerce_variants_listing ON commerce_variants(project_id, listing_id, sort_order, id);
CREATE INDEX ix_commerce_variants_inventory ON commerce_variants(project_id, inventory_item_id);
CREATE INDEX ix_commerce_variants_price ON commerce_variants(project_id, catalog_price_id);

CREATE TABLE commerce_collections (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL REFERENCES commerce_stores(id) ON DELETE CASCADE,
  handle                TEXT    NOT NULL,
  title                 TEXT    NOT NULL,
  description_html       TEXT    NOT NULL DEFAULT '',
  status                TEXT    NOT NULL DEFAULT 'active',
  sort_order            INTEGER NOT NULL DEFAULT 0,
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, store_id, handle)
);

CREATE TABLE commerce_collection_listings (
  collection_id         INTEGER NOT NULL REFERENCES commerce_collections(id) ON DELETE CASCADE,
  listing_id            INTEGER NOT NULL REFERENCES commerce_listings(id) ON DELETE CASCADE,
  sort_order            INTEGER NOT NULL DEFAULT 0,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(collection_id, listing_id)
);

CREATE TABLE commerce_carts (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL REFERENCES commerce_stores(id) ON DELETE CASCADE,
  checkout_cart_id       INTEGER,
  session_token          TEXT    NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'open',
  subtotal_cents         INTEGER NOT NULL DEFAULT 0,
  discount_cents         INTEGER NOT NULL DEFAULT 0,
  tax_cents              INTEGER NOT NULL DEFAULT 0,
  shipping_cents         INTEGER NOT NULL DEFAULT 0,
  total_cents            INTEGER NOT NULL DEFAULT 0,
  currency              TEXT    NOT NULL DEFAULT 'USD',
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at            TIMESTAMP,
  UNIQUE(project_id, store_id, session_token)
);

CREATE INDEX ix_commerce_carts_status ON commerce_carts(project_id, store_id, status, updated_at DESC);

CREATE TABLE commerce_cart_items (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  cart_id               INTEGER NOT NULL REFERENCES commerce_carts(id) ON DELETE CASCADE,
  checkout_item_id       INTEGER,
  variant_id            INTEGER NOT NULL REFERENCES commerce_variants(id),
  listing_id            INTEGER NOT NULL REFERENCES commerce_listings(id),
  inventory_item_id      INTEGER,
  catalog_price_id       INTEGER,
  sku                   TEXT    NOT NULL DEFAULT '',
  title_snapshot         TEXT    NOT NULL,
  unit_amount_cents      INTEGER NOT NULL DEFAULT 0,
  currency              TEXT    NOT NULL DEFAULT 'USD',
  quantity              REAL    NOT NULL DEFAULT 1,
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(cart_id, variant_id)
);

CREATE INDEX ix_commerce_cart_items_cart ON commerce_cart_items(cart_id);

CREATE TABLE commerce_checkout_sessions (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL,
  cart_id               INTEGER NOT NULL REFERENCES commerce_carts(id) ON DELETE CASCADE,
  checkout_session_id    INTEGER,
  status                TEXT    NOT NULL DEFAULT 'started',
  reservation_ids_json   TEXT    NOT NULL DEFAULT '[]',
  invoice_id             INTEGER,
  invoice_number         TEXT    NOT NULL DEFAULT '',
  customer_email         TEXT    NOT NULL DEFAULT '',
  customer_name          TEXT    NOT NULL DEFAULT '',
  shipping_address_json  TEXT    NOT NULL DEFAULT '{}',
  billing_address_json   TEXT    NOT NULL DEFAULT '{}',
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at          TIMESTAMP
);

CREATE INDEX ix_commerce_checkout_status ON commerce_checkout_sessions(project_id, store_id, status, updated_at DESC);

CREATE TABLE commerce_sales (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL,
  cart_id               INTEGER,
  checkout_id            INTEGER,
  checkout_session_id    INTEGER,
  invoice_id             INTEGER,
  invoice_number         TEXT    NOT NULL DEFAULT '',
  order_id               INTEGER,
  status                TEXT    NOT NULL DEFAULT 'awaiting_payment',
  payment_status         TEXT    NOT NULL DEFAULT 'unpaid',
  fulfillment_status     TEXT    NOT NULL DEFAULT 'unsubmitted',
  subtotal_cents         INTEGER NOT NULL DEFAULT 0,
  discount_cents         INTEGER NOT NULL DEFAULT 0,
  tax_cents              INTEGER NOT NULL DEFAULT 0,
  shipping_cents         INTEGER NOT NULL DEFAULT 0,
  total_cents            INTEGER NOT NULL DEFAULT 0,
  currency              TEXT    NOT NULL DEFAULT 'USD',
  customer_email         TEXT    NOT NULL DEFAULT '',
  customer_name          TEXT    NOT NULL DEFAULT '',
  metadata_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  paid_at               TIMESTAMP
);

CREATE INDEX ix_commerce_sales_store ON commerce_sales(project_id, store_id, status, updated_at DESC);
CREATE INDEX ix_commerce_sales_invoice ON commerce_sales(project_id, invoice_id);
