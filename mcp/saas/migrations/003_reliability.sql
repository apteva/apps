-- SaaS v0.2.0 - durable lifecycle, provisioning, fulfillment, and usage sync state.

ALTER TABLE saas_plan_actions
  ADD COLUMN execution_policy TEXT NOT NULL DEFAULT 'once_per_transition';

ALTER TABLE saas_fulfillment_runs
  ADD COLUMN transition_id TEXT NOT NULL DEFAULT '';
ALTER TABLE saas_fulfillment_runs
  ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE saas_fulfillment_runs
  ADD COLUMN started_at TIMESTAMP;
ALTER TABLE saas_fulfillment_runs
  ADD COLUMN completed_at TIMESTAMP;

UPDATE saas_fulfillment_runs
SET transition_id = 'legacy:' || id
WHERE transition_id = '';

CREATE UNIQUE INDEX ux_saas_fulfillment_transition
  ON saas_fulfillment_runs(project_id, account_id, plan_action_id, transition_id);

CREATE TABLE saas_lifecycle_transitions (
  id                    TEXT PRIMARY KEY,
  project_id            TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  sequence              INTEGER NOT NULL,
  event                 TEXT NOT NULL,
  from_status           TEXT NOT NULL DEFAULT '',
  to_status             TEXT NOT NULL DEFAULT '',
  source_key            TEXT NOT NULL DEFAULT '',
  status                TEXT NOT NULL DEFAULT 'pending',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at          TIMESTAMP,
  UNIQUE(project_id, account_id, sequence)
);

CREATE UNIQUE INDEX ux_saas_lifecycle_source
  ON saas_lifecycle_transitions(project_id, account_id, source_key)
  WHERE source_key != '' AND status = 'pending';
CREATE INDEX ix_saas_lifecycle_account
  ON saas_lifecycle_transitions(project_id, account_id, sequence DESC);

CREATE TABLE saas_provisioning_steps (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  step                  TEXT NOT NULL,
  status                TEXT NOT NULL DEFAULT 'pending',
  attempt_count         INTEGER NOT NULL DEFAULT 0,
  output_json           TEXT NOT NULL DEFAULT '{}',
  last_error            TEXT NOT NULL DEFAULT '',
  started_at            TIMESTAMP,
  completed_at          TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, account_id, step)
);

CREATE INDEX ix_saas_provisioning_account
  ON saas_provisioning_steps(project_id, account_id, id);

ALTER TABLE saas_usage_snapshots RENAME TO saas_usage_snapshots_v1;

CREATE TABLE saas_usage_snapshots (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  customer_id           INTEGER NOT NULL,
  usage_source_id       INTEGER,
  source_key            TEXT NOT NULL,
  source_app            TEXT NOT NULL DEFAULT 'manual',
  feature_key           TEXT NOT NULL,
  quantity              INTEGER NOT NULL DEFAULT 0,
  generation_id         TEXT NOT NULL DEFAULT '',
  metadata_json         TEXT NOT NULL DEFAULT '{}',
  observed_at           TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, account_id, source_key, feature_key)
);

INSERT INTO saas_usage_snapshots
  (id, project_id, account_id, customer_id, source_key, source_app, feature_key,
   quantity, generation_id, metadata_json, observed_at, updated_at)
SELECT id, project_id, account_id, customer_id, source_app, source_app, feature_key,
       quantity, 'legacy', metadata_json, observed_at, updated_at
FROM saas_usage_snapshots_v1;

DROP TABLE saas_usage_snapshots_v1;

CREATE INDEX ix_saas_usage_account
  ON saas_usage_snapshots(project_id, account_id, feature_key);
CREATE INDEX ix_saas_usage_generation
  ON saas_usage_snapshots(project_id, account_id, source_key, generation_id);

CREATE TABLE saas_usage_source_state (
  project_id            TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  usage_source_id       INTEGER NOT NULL,
  last_generation_id    TEXT NOT NULL DEFAULT '',
  last_success_at       TIMESTAMP,
  last_error            TEXT NOT NULL DEFAULT '',
  failure_count         INTEGER NOT NULL DEFAULT 0,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, account_id, usage_source_id)
);

CREATE INDEX ix_saas_usage_source_freshness
  ON saas_usage_source_state(project_id, account_id, last_success_at);
