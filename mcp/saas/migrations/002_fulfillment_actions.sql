-- SaaS v0.1.4 — generic lifecycle fulfillment actions.

CREATE TABLE IF NOT EXISTS saas_plan_actions (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  plan_key              TEXT    NOT NULL,
  event                 TEXT    NOT NULL,
  app_name              TEXT    NOT NULL,
  tool_name             TEXT    NOT NULL,
  args_json             TEXT    NOT NULL DEFAULT '{}',
  store_json            TEXT    NOT NULL DEFAULT '{}',
  failure_mode          TEXT    NOT NULL DEFAULT 'fail_account',
  enabled               INTEGER NOT NULL DEFAULT 1,
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_saas_plan_actions_lookup
  ON saas_plan_actions(project_id, plan_key, event, enabled);

CREATE TABLE IF NOT EXISTS saas_fulfillment_runs (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  account_id            TEXT    NOT NULL,
  plan_action_id        INTEGER NOT NULL,
  event                 TEXT    NOT NULL,
  app_name              TEXT    NOT NULL,
  tool_name             TEXT    NOT NULL,
  status                TEXT    NOT NULL,
  input_json            TEXT    NOT NULL DEFAULT '{}',
  output_json           TEXT    NOT NULL DEFAULT '{}',
  error                 TEXT    NOT NULL DEFAULT '',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_saas_fulfillment_runs_account
  ON saas_fulfillment_runs(project_id, account_id, id DESC);
