-- LLM Gateway v0.5.0

CREATE TABLE IF NOT EXISTS provider_rates (
  id                                      INTEGER PRIMARY KEY,
  project_id                              TEXT NOT NULL DEFAULT '',
  provider                                TEXT NOT NULL,
  model_id                                TEXT NOT NULL,
  currency                                TEXT NOT NULL DEFAULT 'USD',
  input_microunits_per_million            INTEGER NOT NULL DEFAULT 0,
  output_microunits_per_million           INTEGER NOT NULL DEFAULT 0,
  cached_input_microunits_per_million     INTEGER NOT NULL DEFAULT 0,
  cache_write_microunits_per_million      INTEGER NOT NULL DEFAULT 0,
  request_microunits                      INTEGER NOT NULL DEFAULT 0,
  extra_rates_json                        TEXT NOT NULL DEFAULT '{}',
  source                                  TEXT NOT NULL DEFAULT 'manual',
  source_reference                        TEXT NOT NULL DEFAULT '',
  effective_from                          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  effective_to                            TIMESTAMP,
  created_at                              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_provider_rates_lookup
  ON provider_rates(project_id, provider, model_id, effective_from DESC);

CREATE UNIQUE INDEX IF NOT EXISTS ux_provider_rates_active
  ON provider_rates(project_id, provider, model_id)
  WHERE effective_to IS NULL;
