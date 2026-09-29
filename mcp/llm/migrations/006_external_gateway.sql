-- LLM Gateway v0.6.0
--
-- External-facing gateway: public model aliases and sell-side pricing.
-- New usage_events columns are added in ensureGatewaySchema because SQLite
-- cannot express conditional ALTER TABLE in static SQL.

CREATE TABLE IF NOT EXISTS model_aliases (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT NOT NULL DEFAULT '',
  alias        TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  targets_json TEXT NOT NULL DEFAULT '[]',
  status       TEXT NOT NULL DEFAULT 'active',
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_model_aliases
  ON model_aliases(project_id, alias);

CREATE INDEX IF NOT EXISTS ix_model_aliases_lookup
  ON model_aliases(alias, status);

CREATE TABLE IF NOT EXISTS model_prices (
  id                            INTEGER PRIMARY KEY,
  project_id                    TEXT NOT NULL DEFAULT '',
  plan_id                       TEXT NOT NULL DEFAULT '',
  alias                         TEXT NOT NULL,
  currency                      TEXT NOT NULL DEFAULT 'USD',
  input_microunits_per_million  INTEGER NOT NULL DEFAULT 0,
  output_microunits_per_million INTEGER NOT NULL DEFAULT 0,
  request_microunits            INTEGER NOT NULL DEFAULT 0,
  minimum_charge_microunits     INTEGER NOT NULL DEFAULT 0,
  price_version                 INTEGER NOT NULL DEFAULT 1,
  effective_from                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  effective_to                  TIMESTAMP,
  created_at                    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_model_prices_lookup
  ON model_prices(project_id, plan_id, alias, effective_from DESC);

CREATE UNIQUE INDEX IF NOT EXISTS ux_model_prices_active
  ON model_prices(project_id, plan_id, alias)
  WHERE effective_to IS NULL;
