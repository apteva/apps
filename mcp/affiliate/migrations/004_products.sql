CREATE TABLE IF NOT EXISTS products (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    network_key       TEXT NOT NULL,
    external_id       TEXT NOT NULL,
    offer_id          INTEGER REFERENCES offers(id) ON DELETE SET NULL,
    merchant_id       TEXT NOT NULL DEFAULT '',
    merchant_name     TEXT NOT NULL DEFAULT '',
    name              TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    category          TEXT NOT NULL DEFAULT '',
    brand             TEXT NOT NULL DEFAULT '',
    sku               TEXT NOT NULL DEFAULT '',
    gtin              TEXT NOT NULL DEFAULT '',
    currency          TEXT NOT NULL DEFAULT '',
    price_cents       INTEGER NOT NULL DEFAULT 0,
    sale_price_cents  INTEGER NOT NULL DEFAULT 0,
    image_url         TEXT NOT NULL DEFAULT '',
    destination_url   TEXT NOT NULL DEFAULT '',
    affiliate_url     TEXT NOT NULL DEFAULT '',
    availability      TEXT NOT NULL DEFAULT '',
    raw_json          TEXT NOT NULL DEFAULT '{}',
    last_refreshed_at TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(network_key, external_id)
);

CREATE INDEX IF NOT EXISTS idx_products_search
    ON products(network_key, merchant_name, name, category, brand);

ALTER TABLE links ADD COLUMN product_id INTEGER REFERENCES products(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_links_product
    ON links(network_key, product_id, status, id);
