-- Catalog v0.1.0 — products + prices.
--
-- Modelled after Stripe's API split: one Product, many Prices.
-- - Currency lives on Price, not Product, so the same product sold
--   globally needs N prices (one per currency).
-- - Recurrence lives on Price (interval/interval_count/trial_days) —
--   a single Product can have both one-time setup fees and recurring
--   subscription prices.
-- - Prices are EFFECTIVELY IMMUTABLE for financial fields after
--   create. To change an amount, create a new price and archive the
--   old one. dbPriceUpdate rejects amount/currency/interval changes
--   so historical invoice snapshots stay sound.

-- ── products ─────────────────────────────────────────────────────────
CREATE TABLE products (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,

  name          TEXT    NOT NULL,
  slug          TEXT,                              -- URL-safe handle (optional)
  description   TEXT,

  -- 'one_time' | 'recurring' | 'service' — UI grouping. The actual
  -- one-time-vs-recurring billing behaviour comes from the Price's
  -- interval field, not this column.
  type          TEXT    NOT NULL,
  category      TEXT,                              -- free-text grouping

  image_file_id INTEGER,                           -- pointer into storage app
  color         TEXT,                              -- "#0066ff" for UI pills

  -- Tax classification label. Resolution to actual rate happens at
  -- checkout/invoice time when the tax engine lands.
  tax_category  TEXT,                              -- 'standard'|'reduced'|'zero'|'exempt'|NULL

  -- Stripe forward-compat. NULL on local-only installs forever;
  -- populated when the Stripe mirror sync (billing v0.x) runs.
  external_id   TEXT,                              -- prod_…

  metadata      TEXT    NOT NULL DEFAULT '{}',

  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at   TIMESTAMP                          -- soft-delete
);

-- Per-project slug uniqueness on non-archived rows. Partial index
-- means an archived product's slug can be reused.
CREATE UNIQUE INDEX ux_products_slug
  ON products(project_id, slug)
  WHERE slug IS NOT NULL AND archived_at IS NULL;

CREATE INDEX ix_products_proj ON products(project_id, archived_at);
CREATE INDEX ix_products_type ON products(project_id, type, archived_at);
CREATE INDEX ix_products_ext  ON products(external_id);


-- ── prices ───────────────────────────────────────────────────────────
CREATE TABLE prices (
  id                INTEGER PRIMARY KEY,
  product_id        INTEGER NOT NULL REFERENCES products(id),
  project_id        TEXT    NOT NULL,              -- denormalised for fast project-scoped scans

  nickname          TEXT,                          -- internal label ("Pro monthly")

  unit_amount_cents INTEGER NOT NULL,
  currency          TEXT    NOT NULL,              -- ISO 4217

  -- Recurrence (all NULL/0 → one-time price)
  interval          TEXT,                          -- 'day'|'week'|'month'|'year'|NULL
  interval_count    INTEGER NOT NULL DEFAULT 1,    -- "every N intervals" (usually 1)
  trial_days        INTEGER NOT NULL DEFAULT 0,    -- 0 = no trial

  -- Inactive prices can't be assigned to new invoices/subscriptions;
  -- existing references keep resolving (FK integrity preserved).
  active            BOOLEAN NOT NULL DEFAULT 1,
  -- Whether unit_amount_cents already includes tax (EU B2C) vs is the
  -- pre-tax base (US B2B). Tax engine reads this when computing rates.
  tax_inclusive     BOOLEAN NOT NULL DEFAULT 0,

  external_id       TEXT,                          -- price_… (Stripe mirror)

  metadata          TEXT    NOT NULL DEFAULT '{}',

  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at       TIMESTAMP
);

CREATE INDEX ix_prices_product ON prices(product_id, active, archived_at);
CREATE INDEX ix_prices_proj    ON prices(project_id, archived_at);
CREATE INDEX ix_prices_ext     ON prices(external_id);
