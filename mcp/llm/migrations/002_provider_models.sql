-- LLM Gateway v0.2.0

CREATE TABLE IF NOT EXISTS provider_models (
  id                         INTEGER PRIMARY KEY,
  project_id                 TEXT NOT NULL DEFAULT '',
  provider                   TEXT NOT NULL,
  model_id                   TEXT NOT NULL,
  display_name               TEXT NOT NULL DEFAULT '',
  gateway_model              TEXT NOT NULL DEFAULT '',
  capabilities_json          TEXT NOT NULL DEFAULT '{}',
  context_window             INTEGER NOT NULL DEFAULT 0,
  input_modalities_json      TEXT NOT NULL DEFAULT '[]',
  output_modalities_json     TEXT NOT NULL DEFAULT '[]',
  status                     TEXT NOT NULL DEFAULT 'active',
  raw_json                   TEXT NOT NULL DEFAULT '{}',
  last_seen_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  created_at                 TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at                 TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, provider, model_id)
);

CREATE INDEX IF NOT EXISTS ix_provider_models_project ON provider_models(project_id, provider, status, model_id);
