-- SaaS v0.12.0 -- prepaid plans: usage meters and account API keys.

-- Links a plan's usage meter (e.g. llm / model:text-fast) to the Catalog
-- metered prices for each billable part. Catalog stays the owner of prices.
CREATE TABLE saas_plan_meters (
  id                     INTEGER PRIMARY KEY,
  project_id             TEXT NOT NULL,
  plan_key               TEXT NOT NULL,
  source_app             TEXT NOT NULL,
  meter                  TEXT NOT NULL,
  input_price_id         INTEGER,
  output_price_id        INTEGER,
  cached_input_price_id  INTEGER,
  per_request_price_id   INTEGER,
  metadata_json          TEXT NOT NULL DEFAULT '{}',
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, plan_key, source_app, meter)
);

-- API keys issued for an account through the plan's credential provider.
-- The secret is never stored; only the provider's token id.
CREATE TABLE saas_account_keys (
  id             TEXT PRIMARY KEY,
  project_id     TEXT NOT NULL,
  account_id     TEXT NOT NULL,
  provider_app   TEXT NOT NULL,
  token_id       INTEGER NOT NULL,
  label          TEXT NOT NULL DEFAULT '',
  space          TEXT NOT NULL DEFAULT '',
  display        TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'active',
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  revoked_at     TIMESTAMP
);
CREATE INDEX ix_saas_account_keys_account ON saas_account_keys(project_id, account_id, status);
