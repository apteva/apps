-- Generic API credentials. Domain-specific identity and authorization data is
-- stored as opaque, validated claims; the gateway does not model SaaS plans,
-- billing accounts, or application entitlements.
ALTER TABLE api_keys ADD COLUMN subject_type TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN subject_id TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN claims_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE api_keys ADD COLUMN scopes_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN expires_at TEXT;
ALTER TABLE api_keys ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE api_keys ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN issuance_key TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN issuance_fingerprint TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS ix_api_keys_subject
  ON api_keys(project_id, api_id, subject_type, subject_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_api_keys_external_id
  ON api_keys(project_id, api_id, external_id)
  WHERE external_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS ux_api_keys_issuance_key
  ON api_keys(project_id, api_id, issuance_key)
  WHERE issuance_key <> '';

-- Usage plans are operational traffic policies. They deliberately contain no
-- prices, products, subscriptions, entitlements, or billing state.
CREATE TABLE IF NOT EXISTS api_usage_plans (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id       TEXT NOT NULL,
  api_id           INTEGER NOT NULL,
  name             TEXT NOT NULL,
  rate_limit       INTEGER NOT NULL,
  interval_seconds INTEGER NOT NULL,
  burst            INTEGER NOT NULL,
  created_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, api_id, name),
  FOREIGN KEY(api_id) REFERENCES apis(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS api_key_usage_plans (
  project_id   TEXT NOT NULL,
  api_key_id   INTEGER PRIMARY KEY,
  usage_plan_id INTEGER NOT NULL,
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE,
  FOREIGN KEY(usage_plan_id) REFERENCES api_usage_plans(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_api_key_usage_plans_plan
  ON api_key_usage_plans(project_id, usage_plan_id);
