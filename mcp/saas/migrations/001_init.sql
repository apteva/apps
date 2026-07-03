-- SaaS v0.1.0 — shared multi-tenant app access with live usage gauges.

CREATE TABLE IF NOT EXISTS saas_customers (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  email                 TEXT    NOT NULL,
  name                  TEXT    NOT NULL DEFAULT '',
  billing_customer_id   INTEGER,
  auth_user_id          INTEGER,
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, email)
);

CREATE TABLE IF NOT EXISTS saas_plans (
  key                   TEXT    NOT NULL,
  project_id            TEXT    NOT NULL,
  name                  TEXT    NOT NULL,
  billing_mode          TEXT    NOT NULL DEFAULT 'free',
  catalog_product_id    INTEGER,
  catalog_price_id      INTEGER,
  subscription_required INTEGER NOT NULL DEFAULT 0,
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, key)
);

CREATE TABLE IF NOT EXISTS saas_plan_features (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  plan_key              TEXT    NOT NULL,
  feature_key           TEXT    NOT NULL,
  grant_type            TEXT    NOT NULL DEFAULT 'access',
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, plan_key, feature_key)
);

CREATE TABLE IF NOT EXISTS saas_plan_limits (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  plan_key              TEXT    NOT NULL,
  feature_key           TEXT    NOT NULL,
  limit_value           INTEGER NOT NULL DEFAULT 0,
  reset_interval        TEXT    NOT NULL DEFAULT 'none',
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, plan_key, feature_key)
);

CREATE TABLE IF NOT EXISTS saas_usage_sources (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  plan_key              TEXT    NOT NULL,
  app_name              TEXT    NOT NULL,
  tool_name             TEXT    NOT NULL,
  feature_prefix        TEXT    NOT NULL DEFAULT '',
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, plan_key, app_name, tool_name)
);

CREATE TABLE IF NOT EXISTS saas_accounts (
  id                    TEXT PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  customer_id           INTEGER NOT NULL,
  auth_org_id           INTEGER,
  auth_user_id          INTEGER,
  subscription_id       INTEGER,
  slug                  TEXT    NOT NULL,
  owner_email           TEXT    NOT NULL,
  plan_key              TEXT    NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'provisioning',
  last_usage_sync_at    TIMESTAMP,
  last_error            TEXT    NOT NULL DEFAULT '',
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, slug)
);

CREATE INDEX IF NOT EXISTS ix_saas_accounts_customer
  ON saas_accounts(project_id, customer_id, status);
CREATE INDEX IF NOT EXISTS ix_saas_accounts_plan
  ON saas_accounts(project_id, plan_key, status);
CREATE INDEX IF NOT EXISTS ix_saas_accounts_subscription
  ON saas_accounts(project_id, subscription_id)
  WHERE subscription_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS saas_usage_snapshots (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  account_id            TEXT    NOT NULL,
  customer_id           INTEGER NOT NULL,
  source_app            TEXT    NOT NULL DEFAULT 'manual',
  feature_key           TEXT    NOT NULL,
  quantity              INTEGER NOT NULL DEFAULT 0,
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  observed_at           TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, account_id, source_app, feature_key)
);

CREATE INDEX IF NOT EXISTS ix_saas_usage_account
  ON saas_usage_snapshots(project_id, account_id, feature_key);

CREATE TABLE IF NOT EXISTS saas_events (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  account_id            TEXT    NOT NULL DEFAULT '',
  event_type            TEXT    NOT NULL,
  actor                 TEXT    NOT NULL DEFAULT 'system',
  payload_json          TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_saas_events_account
  ON saas_events(project_id, account_id, id DESC);
