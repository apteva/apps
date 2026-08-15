ALTER TABLE products ADD COLUMN source TEXT NOT NULL DEFAULT 'provider'
    CHECK(source IN ('provider', 'manual'));

ALTER TABLE products ADD COLUMN status TEXT NOT NULL DEFAULT 'active'
    CHECK(status IN ('active', 'archived'));

CREATE INDEX IF NOT EXISTS idx_products_status
    ON products(status, source, network_key, updated_at DESC);
