-- SaaS v0.5.0 -- durable same-product plan changes.

CREATE TABLE saas_plan_changes (
  id                     TEXT PRIMARY KEY,
  project_id             TEXT NOT NULL,
  account_id             TEXT NOT NULL,
  subscription_id        INTEGER NOT NULL,
  idempotency_key        TEXT NOT NULL,
  request_fingerprint    TEXT NOT NULL,
  from_plan_key          TEXT NOT NULL,
  target_plan_key        TEXT NOT NULL,
  change_kind            TEXT NOT NULL,
  effective_mode         TEXT NOT NULL,
  proration_policy       TEXT NOT NULL,
  discount_policy        TEXT NOT NULL DEFAULT 'preserve',
  subscription_change_id INTEGER,
  billing_customer_id    INTEGER,
  invoice_id             INTEGER,
  status                 TEXT NOT NULL DEFAULT 'pending',
  stage                  TEXT NOT NULL DEFAULT 'pending',
  proration_json         TEXT NOT NULL DEFAULT '{}',
  payment_link_json      TEXT NOT NULL DEFAULT '{}',
  success_url            TEXT NOT NULL DEFAULT '',
  cancel_url             TEXT NOT NULL DEFAULT '',
  last_error             TEXT NOT NULL DEFAULT '',
  attempt_count          INTEGER NOT NULL DEFAULT 0,
  lease_until            TIMESTAMP,
  next_attempt_at        TIMESTAMP,
  completed_at           TIMESTAMP,
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK (change_kind IN ('upgrade', 'downgrade', 'lateral')),
  CHECK (effective_mode IN ('immediate', 'next_cycle')),
  CHECK (proration_policy IN ('none', 'prorate', 'charge_full')),
  CHECK (discount_policy IN ('preserve', 'drop')),
  CHECK (status IN ('pending', 'scheduled', 'awaiting_payment', 'applying', 'applied', 'failed')),
  UNIQUE(project_id, idempotency_key)
);

CREATE UNIQUE INDEX ux_saas_plan_change_open
  ON saas_plan_changes(project_id, account_id)
  WHERE status IN ('pending', 'scheduled', 'awaiting_payment', 'applying', 'failed');

CREATE UNIQUE INDEX ux_saas_plan_change_subscription_change
  ON saas_plan_changes(project_id, subscription_change_id)
  WHERE subscription_change_id IS NOT NULL;

CREATE UNIQUE INDEX ux_saas_plan_change_invoice
  ON saas_plan_changes(project_id, invoice_id)
  WHERE invoice_id IS NOT NULL;

CREATE INDEX ix_saas_plan_change_status
  ON saas_plan_changes(project_id, status, updated_at);
