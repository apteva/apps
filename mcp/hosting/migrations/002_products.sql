-- Hosting v1.1.0 — catalog-backed hosting products and runtime templates.

CREATE TABLE IF NOT EXISTS hosting_products (
  key                 TEXT PRIMARY KEY,
  catalog_product_id  INTEGER,
  name                TEXT    NOT NULL,
  description         TEXT    NOT NULL DEFAULT '',
  runtime_kind        TEXT    NOT NULL DEFAULT 'single_container',
  status              TEXT    NOT NULL DEFAULT 'active',
  template_json       TEXT    NOT NULL DEFAULT '{}',
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosting_product_versions (
  id                  INTEGER PRIMARY KEY,
  product_key         TEXT    NOT NULL REFERENCES hosting_products(key) ON DELETE CASCADE,
  version             TEXT    NOT NULL,
  image               TEXT    NOT NULL DEFAULT '',
  default_port        INTEGER NOT NULL DEFAULT 80,
  default_health_path TEXT    NOT NULL DEFAULT '/',
  template_json       TEXT    NOT NULL DEFAULT '{}',
  status              TEXT    NOT NULL DEFAULT 'active',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(product_key, version)
);

CREATE INDEX IF NOT EXISTS ix_hosting_product_versions_product
  ON hosting_product_versions(product_key, status);

CREATE TABLE IF NOT EXISTS hosting_plan_bindings (
  plan_key              TEXT PRIMARY KEY REFERENCES hosting_plans(key) ON DELETE CASCADE,
  product_key           TEXT    NOT NULL REFERENCES hosting_products(key),
  catalog_price_id      INTEGER,
  subscription_required INTEGER NOT NULL DEFAULT 0,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_hosting_plan_bindings_product
  ON hosting_plan_bindings(product_key);
