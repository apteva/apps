-- Subscriptions v0.1.0 — generic recurring commerce.

CREATE TABLE subscriptions (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,

  customer_id            INTEGER,
  customer_email         TEXT,
  customer_name          TEXT,

  kind                  TEXT    NOT NULL DEFAULT 'saas', -- saas | physical | service
  status                TEXT    NOT NULL DEFAULT 'active',

  billing_provider       TEXT    NOT NULL DEFAULT 'local',
  external_id            TEXT,

  currency              TEXT    NOT NULL DEFAULT 'USD',
  interval              TEXT    NOT NULL DEFAULT 'month',
  interval_count         INTEGER NOT NULL DEFAULT 1,
  quantity              REAL    NOT NULL DEFAULT 1,

  trial_start            TIMESTAMP,
  trial_end              TIMESTAMP,
  current_period_start   TIMESTAMP,
  current_period_end     TIMESTAMP,
  next_renewal_at        TIMESTAMP,
  cancel_at              TIMESTAMP,
  cancelled_at           TIMESTAMP,
  ended_at               TIMESTAMP,

  source                 TEXT    NOT NULL DEFAULT 'manual',
  source_ref             TEXT,
  metadata               TEXT    NOT NULL DEFAULT '{}',

  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_subscriptions_external ON subscriptions(project_id, billing_provider, external_id)
  WHERE external_id IS NOT NULL AND external_id != '';
CREATE INDEX ix_subscriptions_customer ON subscriptions(project_id, customer_id, status)
  WHERE customer_id IS NOT NULL;
CREATE INDEX ix_subscriptions_email ON subscriptions(project_id, customer_email, status)
  WHERE customer_email IS NOT NULL;
CREATE INDEX ix_subscriptions_status ON subscriptions(project_id, kind, status, next_renewal_at);

CREATE TABLE subscription_items (
  id                    INTEGER PRIMARY KEY,
  subscription_id        INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  position              INTEGER NOT NULL DEFAULT 0,

  catalog_product_id     INTEGER,
  catalog_price_id       INTEGER,
  sku                   TEXT,
  title                 TEXT    NOT NULL,
  quantity              REAL    NOT NULL DEFAULT 1,
  unit_amount_cents     INTEGER NOT NULL DEFAULT 0,
  currency              TEXT    NOT NULL DEFAULT 'USD',
  metadata              TEXT    NOT NULL DEFAULT '{}',

  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_subscription_items_sub ON subscription_items(subscription_id, position);
CREATE INDEX ix_subscription_items_catalog ON subscription_items(catalog_product_id, catalog_price_id);

CREATE TABLE subscription_cycles (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subscription_id        INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  cycle_number           INTEGER NOT NULL,

  period_start           TIMESTAMP NOT NULL,
  period_end             TIMESTAMP NOT NULL,
  due_at                 TIMESTAMP,

  invoice_id             INTEGER,
  order_id               INTEGER,
  entitlement_grant_id   INTEGER,

  payment_status         TEXT    NOT NULL DEFAULT 'pending',
  fulfillment_status     TEXT    NOT NULL DEFAULT 'none',

  subtotal_cents         INTEGER NOT NULL DEFAULT 0,
  tax_cents              INTEGER NOT NULL DEFAULT 0,
  shipping_cents         INTEGER NOT NULL DEFAULT 0,
  total_cents            INTEGER NOT NULL DEFAULT 0,
  currency               TEXT    NOT NULL DEFAULT 'USD',

  metadata               TEXT    NOT NULL DEFAULT '{}',
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  paid_at                TIMESTAMP,
  completed_at           TIMESTAMP
);

CREATE UNIQUE INDEX ux_cycles_number ON subscription_cycles(subscription_id, cycle_number);
CREATE INDEX ix_cycles_sub ON subscription_cycles(subscription_id, created_at DESC);
CREATE INDEX ix_cycles_status ON subscription_cycles(project_id, payment_status, fulfillment_status, updated_at DESC);

CREATE TABLE subscription_events (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subscription_id        INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  actor                 TEXT    NOT NULL DEFAULT 'system',
  action                TEXT    NOT NULL,
  details               TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_subscription_events_sub ON subscription_events(subscription_id, created_at DESC);
