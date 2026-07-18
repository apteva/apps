-- SaaS v0.4.0 - durable Catalog discount orchestration.

ALTER TABLE saas_plans
  ADD COLUMN catalog_discount_id INTEGER;

CREATE TABLE saas_checkout_discounts (
  project_id          TEXT NOT NULL,
  checkout_id         TEXT NOT NULL,
  reservation_id      TEXT NOT NULL,
  reservation_key     TEXT NOT NULL,
  catalog_discount_id INTEGER NOT NULL,
  discount_code       TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL,
  application_json    TEXT NOT NULL,
  currency            TEXT NOT NULL,
  subtotal_cents      INTEGER NOT NULL,
  discount_cents      INTEGER NOT NULL,
  total_cents         INTEGER NOT NULL,
  expires_at          TIMESTAMP,
  attempt_count       INTEGER NOT NULL DEFAULT 1,
  last_error          TEXT NOT NULL DEFAULT '',
  redeemed_at         TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, checkout_id),
  UNIQUE(project_id, reservation_id),
  CHECK (status IN ('reserved', 'redeemed', 'released', 'expired', 'failed')),
  CHECK (subtotal_cents >= 0),
  CHECK (discount_cents >= 0),
  CHECK (total_cents >= 0)
);

CREATE INDEX ix_saas_checkout_discounts_reconcile
  ON saas_checkout_discounts(project_id, status, updated_at);
