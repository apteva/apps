-- Generic metered subscription items and usage ledger.

ALTER TABLE subscription_items ADD COLUMN billing_scheme TEXT NOT NULL DEFAULT 'flat';
ALTER TABLE subscription_items ADD COLUMN meter_key TEXT NOT NULL DEFAULT '';
ALTER TABLE subscription_items ADD COLUMN included_units INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscription_items ADD COLUMN unit_size INTEGER NOT NULL DEFAULT 1;
ALTER TABLE subscription_items ADD COLUMN status TEXT NOT NULL DEFAULT 'active';

CREATE INDEX ix_subscription_items_meter
  ON subscription_items(subscription_id, billing_scheme, meter_key, status);

CREATE TABLE subscription_usage_records (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subscription_id        INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  subscription_item_id   INTEGER NOT NULL REFERENCES subscription_items(id) ON DELETE CASCADE,
  meter_key             TEXT    NOT NULL,
  subject_type          TEXT    NOT NULL DEFAULT '',
  subject_id            TEXT    NOT NULL DEFAULT '',
  quantity              INTEGER NOT NULL,
  occurred_at           TIMESTAMP NOT NULL,
  idempotency_key       TEXT    NOT NULL,
  metadata              TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_subscription_usage_idempotency
  ON subscription_usage_records(project_id, idempotency_key);
CREATE INDEX ix_subscription_usage_item_period
  ON subscription_usage_records(subscription_item_id, occurred_at);
CREATE INDEX ix_subscription_usage_subject
  ON subscription_usage_records(project_id, subject_type, subject_id, meter_key, occurred_at);
