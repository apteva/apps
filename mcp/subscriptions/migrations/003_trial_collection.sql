-- Durable paid-trial conversion and collection state.

ALTER TABLE subscriptions ADD COLUMN billing_customer_id INTEGER;
ALTER TABLE subscriptions ADD COLUMN collection_method TEXT NOT NULL DEFAULT 'invoice';
ALTER TABLE subscriptions ADD COLUMN collection_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE subscriptions ADD COLUMN trial_end_behavior TEXT NOT NULL DEFAULT 'collect';
ALTER TABLE subscriptions ADD COLUMN last_collection_error TEXT;
ALTER TABLE subscriptions ADD COLUMN collection_invoice_id INTEGER;

CREATE INDEX ix_subscriptions_collection
  ON subscriptions(project_id, collection_status, status, trial_end);

ALTER TABLE subscription_cycles ADD COLUMN lifecycle_attempt_id INTEGER;

CREATE UNIQUE INDEX ux_cycles_lifecycle_attempt
  ON subscription_cycles(lifecycle_attempt_id)
  WHERE lifecycle_attempt_id IS NOT NULL;

CREATE TABLE subscription_lifecycle_attempts (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT NOT NULL,
  subscription_id     INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  action              TEXT NOT NULL,
  effective_at        TIMESTAMP NOT NULL,
  status              TEXT NOT NULL DEFAULT 'pending',
  attempt_count       INTEGER NOT NULL DEFAULT 0,
  next_attempt_at     TIMESTAMP,
  lease_until         TIMESTAMP,
  billing_customer_id INTEGER,
  invoice_id          INTEGER,
  cycle_id            INTEGER,
  collection_ref      TEXT,
  result              TEXT NOT NULL DEFAULT '{}',
  last_error          TEXT,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at        TIMESTAMP
);

CREATE UNIQUE INDEX ux_subscription_lifecycle_action
  ON subscription_lifecycle_attempts(project_id, subscription_id, action, effective_at);

CREATE INDEX ix_subscription_lifecycle_due
  ON subscription_lifecycle_attempts(project_id, status, next_attempt_at, lease_until, effective_at);
