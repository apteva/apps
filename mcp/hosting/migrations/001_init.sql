-- Hosting v1.0.0 — managed container-backed Apteva tenants.

CREATE TABLE IF NOT EXISTS hosting_customers (
  id                  INTEGER PRIMARY KEY,
  email               TEXT    NOT NULL UNIQUE,
  name                TEXT    NOT NULL DEFAULT '',
  billing_customer_id INTEGER,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosting_plans (
  key                 TEXT PRIMARY KEY,
  name                TEXT    NOT NULL,
  billing_mode        TEXT    NOT NULL DEFAULT 'free',
  price_cents         INTEGER,
  interval            TEXT,
  image               TEXT    NOT NULL DEFAULT '',
  cpu                 REAL    NOT NULL DEFAULT 1,
  memory_mb           INTEGER NOT NULL DEFAULT 1024,
  storage_mb          INTEGER NOT NULL DEFAULT 1024,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosting_plan_limits (
  id                  INTEGER PRIMARY KEY,
  plan_key            TEXT    NOT NULL REFERENCES hosting_plans(key) ON DELETE CASCADE,
  feature_key         TEXT    NOT NULL,
  limit_value         INTEGER NOT NULL,
  reset_interval      TEXT    NOT NULL DEFAULT 'none',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(plan_key, feature_key)
);

CREATE TABLE IF NOT EXISTS hosting_tenants (
  id                  TEXT PRIMARY KEY,
  customer_id         INTEGER NOT NULL REFERENCES hosting_customers(id),
  subscription_id     INTEGER,
  workload_id         TEXT    NOT NULL DEFAULT '',
  slug                TEXT    NOT NULL UNIQUE,
  default_hostname    TEXT    NOT NULL UNIQUE,
  owner_email         TEXT    NOT NULL,
  plan_key            TEXT    NOT NULL REFERENCES hosting_plans(key),
  status              TEXT    NOT NULL DEFAULT 'provisioning',
  apteva_version      TEXT    NOT NULL DEFAULT '',
  image               TEXT    NOT NULL DEFAULT '',
  api_key_enc         TEXT    NOT NULL DEFAULT '',
  last_health_status  TEXT    NOT NULL DEFAULT 'unknown',
  last_error          TEXT    NOT NULL DEFAULT '',
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_hosting_tenants_customer
  ON hosting_tenants(customer_id, status);
CREATE INDEX IF NOT EXISTS ix_hosting_tenants_plan
  ON hosting_tenants(plan_key, status);

CREATE TABLE IF NOT EXISTS hosting_usage_events (
  id                  INTEGER PRIMARY KEY,
  tenant_id           TEXT,
  customer_id         INTEGER NOT NULL,
  feature_key         TEXT    NOT NULL,
  quantity            INTEGER NOT NULL DEFAULT 1,
  idempotency_key     TEXT,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  occurred_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_usage_idempotency
  ON hosting_usage_events(customer_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
CREATE INDEX IF NOT EXISTS ix_hosting_usage_customer_feature
  ON hosting_usage_events(customer_id, feature_key, occurred_at DESC);
CREATE INDEX IF NOT EXISTS ix_hosting_usage_tenant_feature
  ON hosting_usage_events(tenant_id, feature_key, occurred_at DESC);

CREATE TABLE IF NOT EXISTS hosting_events (
  id                  INTEGER PRIMARY KEY,
  tenant_id           TEXT NOT NULL DEFAULT '',
  kind                TEXT NOT NULL,
  actor               TEXT NOT NULL DEFAULT '',
  payload_json        TEXT NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_hosting_events_tenant
  ON hosting_events(tenant_id, id DESC);
