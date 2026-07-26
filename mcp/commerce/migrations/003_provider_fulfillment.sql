-- Commerce v0.3.0 - minimal external product sourcing.

CREATE TABLE commerce_provider_policies (
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL REFERENCES commerce_stores(id) ON DELETE CASCADE,
  connection_id         INTEGER NOT NULL,
  provider_slug         TEXT    NOT NULL,
  enabled               INTEGER NOT NULL DEFAULT 1,
  fulfillment_mode      TEXT    NOT NULL DEFAULT 'review',
  margin_bps            INTEGER NOT NULL DEFAULT 3000,
  settings_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, store_id, connection_id)
);

CREATE INDEX ix_commerce_provider_policies_store
  ON commerce_provider_policies(project_id, store_id, enabled);

CREATE TABLE commerce_variant_sources (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL REFERENCES commerce_stores(id) ON DELETE CASCADE,
  variant_id            INTEGER NOT NULL REFERENCES commerce_variants(id) ON DELETE CASCADE,
  connection_id         INTEGER NOT NULL,
  provider_slug         TEXT    NOT NULL,
  external_product_id   TEXT    NOT NULL,
  external_variant_id   TEXT    NOT NULL,
  provider_sku          TEXT    NOT NULL DEFAULT '',
  unit_cost_cents       INTEGER NOT NULL DEFAULT 0,
  currency              TEXT    NOT NULL DEFAULT 'USD',
  availability          TEXT    NOT NULL DEFAULT 'unknown',
  available_quantity    REAL,
  source_json           TEXT    NOT NULL DEFAULT '{}',
  last_synced_at        TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, variant_id),
  UNIQUE(project_id, connection_id, external_variant_id)
);

CREATE INDEX ix_commerce_variant_sources_provider
  ON commerce_variant_sources(project_id, connection_id, availability);

CREATE TABLE commerce_dispatch_jobs (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  store_id              INTEGER NOT NULL,
  sale_id               INTEGER NOT NULL REFERENCES commerce_sales(id) ON DELETE CASCADE,
  order_id              INTEGER NOT NULL,
  fulfillment_id        INTEGER,
  connection_id         INTEGER NOT NULL,
  provider_slug         TEXT    NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'review',
  idempotency_key       TEXT    NOT NULL,
  external_order_id     TEXT    NOT NULL DEFAULT '',
  request_json          TEXT    NOT NULL DEFAULT '{}',
  response_json         TEXT    NOT NULL DEFAULT '{}',
  error                 TEXT    NOT NULL DEFAULT '',
  attempt_count         INTEGER NOT NULL DEFAULT 0,
  next_attempt_at       TIMESTAMP,
  submitted_at          TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, idempotency_key)
);

CREATE INDEX ix_commerce_dispatch_jobs_status
  ON commerce_dispatch_jobs(project_id, status, next_attempt_at, updated_at);
