-- SaaS v0.3.1 - durable quota state used to emit transition-only events.

CREATE TABLE saas_quota_states (
  project_id            TEXT NOT NULL,
  account_id            TEXT NOT NULL,
  plan_key              TEXT NOT NULL,
  feature_key           TEXT NOT NULL,
  state                 TEXT NOT NULL DEFAULT 'below',
  quantity              INTEGER NOT NULL DEFAULT 0,
  limit_value           INTEGER NOT NULL DEFAULT 0,
  threshold_percent     INTEGER NOT NULL DEFAULT 80,
  changed_at            TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, account_id, feature_key)
);

CREATE INDEX ix_saas_quota_state
  ON saas_quota_states(project_id, state, updated_at);
