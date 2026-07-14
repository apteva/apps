-- Subscriptions v0.5.0 -- durable, cycle-safe subscription item changes.

ALTER TABLE subscription_items
  ADD COLUMN starts_cycle_number INTEGER NOT NULL DEFAULT 1;

ALTER TABLE subscription_items
  ADD COLUMN ends_cycle_number INTEGER NOT NULL DEFAULT 0;

CREATE INDEX ix_subscription_items_cycle_range
  ON subscription_items(subscription_id, starts_cycle_number, ends_cycle_number);

CREATE TABLE subscription_changes (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subscription_id       INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  idempotency_key       TEXT    NOT NULL,
  request_fingerprint   TEXT    NOT NULL,
  source_app            TEXT    NOT NULL DEFAULT 'manual',
  source_ref            TEXT    NOT NULL DEFAULT '',
  status                TEXT    NOT NULL DEFAULT 'pending',
  effective_mode        TEXT    NOT NULL,
  effective_at          TIMESTAMP NOT NULL,
  proration_policy      TEXT    NOT NULL,
  discount_policy       TEXT    NOT NULL DEFAULT 'preserve',
  old_items_json        TEXT    NOT NULL,
  new_items_json        TEXT    NOT NULL,
  proration_json        TEXT    NOT NULL DEFAULT '{}',
  interval              TEXT    NOT NULL DEFAULT '',
  interval_count        INTEGER NOT NULL DEFAULT 0,
  last_error            TEXT    NOT NULL DEFAULT '',
  attempt_count         INTEGER NOT NULL DEFAULT 0,
  lease_until           TIMESTAMP,
  next_attempt_at       TIMESTAMP,
  applied_at            TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK (status IN ('pending', 'awaiting_approval', 'processing', 'applied', 'cancelled', 'failed')),
  CHECK (effective_mode IN ('immediate', 'next_cycle')),
  CHECK (proration_policy IN ('none', 'prorate', 'charge_full')),
  CHECK (discount_policy IN ('preserve', 'drop')),
  UNIQUE(project_id, idempotency_key)
);

CREATE UNIQUE INDEX ux_subscription_change_pending
  ON subscription_changes(project_id, subscription_id)
  WHERE status IN ('pending', 'awaiting_approval', 'processing', 'failed');

CREATE INDEX ix_subscription_changes_due
  ON subscription_changes(project_id, status, effective_at);

CREATE INDEX ix_subscription_changes_source
  ON subscription_changes(project_id, source_app, source_ref)
  WHERE source_ref != '';
