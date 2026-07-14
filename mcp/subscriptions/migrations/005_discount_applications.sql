-- Subscriptions v0.4.0 -- immutable recurring discount applications.

CREATE TABLE subscription_discounts (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  subscription_id          INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  subscription_item_id     INTEGER NOT NULL REFERENCES subscription_items(id) ON DELETE CASCADE,
  source_app               TEXT    NOT NULL,
  source_ref               TEXT    NOT NULL,
  catalog_discount_id      INTEGER NOT NULL DEFAULT 0,
  catalog_code_id          INTEGER NOT NULL DEFAULT 0,
  code                     TEXT    NOT NULL DEFAULT '',
  name                     TEXT    NOT NULL,
  discount_type            TEXT    NOT NULL,
  percentage_bps           INTEGER NOT NULL DEFAULT 0,
  value_cents              INTEGER NOT NULL DEFAULT 0,
  currency                 TEXT    NOT NULL DEFAULT '',
  duration                 TEXT    NOT NULL,
  duration_cycles          INTEGER NOT NULL DEFAULT 0,
  starts_cycle_number      INTEGER NOT NULL,
  ends_cycle_number        INTEGER NOT NULL DEFAULT 0,
  status                   TEXT    NOT NULL DEFAULT 'active',
  application_json         TEXT    NOT NULL,
  metadata                 TEXT    NOT NULL DEFAULT '{}',
  created_at               TEXT    NOT NULL,
  updated_at               TEXT    NOT NULL,
  cancelled_at             TEXT    NOT NULL DEFAULT '',
  CHECK (discount_type IN ('percentage', 'amount', 'price_override')),
  CHECK (duration IN ('once', 'repeating', 'forever')),
  CHECK (status IN ('active', 'cancelled')),
  CHECK (percentage_bps >= 0 AND percentage_bps <= 10000),
  CHECK (value_cents >= 0),
  CHECK (duration_cycles >= 0),
  CHECK (starts_cycle_number >= 1),
  CHECK (ends_cycle_number >= 0),
  UNIQUE(project_id, source_app, source_ref)
);

CREATE INDEX ix_subscription_discounts_item
  ON subscription_discounts(project_id, subscription_id, subscription_item_id, starts_cycle_number);

CREATE INDEX ix_subscription_discounts_subscription
  ON subscription_discounts(project_id, subscription_id, status, starts_cycle_number);

ALTER TABLE subscription_cycles
  ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0;
