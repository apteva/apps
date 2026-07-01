-- LLM Gateway v0.1.0

CREATE TABLE provider_configs (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  base_url      TEXT NOT NULL DEFAULT '',
  auth_mode     TEXT NOT NULL DEFAULT 'platform_shared',
  connection_id INTEGER NOT NULL DEFAULT 0,
  key_ref       TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  priority      INTEGER NOT NULL DEFAULT 100,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, provider)
);

CREATE INDEX ix_provider_configs_project ON provider_configs(project_id, enabled, priority);

CREATE TABLE provider_models (
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

CREATE INDEX ix_provider_models_project ON provider_models(project_id, provider, status, model_id);

CREATE TABLE policies (
  id                     INTEGER PRIMARY KEY,
  project_id             TEXT NOT NULL UNIQUE,
  allowed_models_json    TEXT NOT NULL DEFAULT '[]',
  blocked_models_json    TEXT NOT NULL DEFAULT '[]',
  allowed_providers_json TEXT NOT NULL DEFAULT '[]',
  limits_json            TEXT NOT NULL DEFAULT '{}',
  fallback_policy_json   TEXT NOT NULL DEFAULT '{}',
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT NOT NULL DEFAULT '',
  subject_type TEXT NOT NULL DEFAULT 'project',
  subject_id   TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  scopes_json  TEXT NOT NULL DEFAULT '[]',
  expires_at   TIMESTAMP,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  revoked_at   TIMESTAMP
);

CREATE INDEX ix_api_tokens_subject ON api_tokens(project_id, subject_type, subject_id);

CREATE TABLE usage_events (
  id                   INTEGER PRIMARY KEY,
  project_id            TEXT NOT NULL DEFAULT '',
  subject_type          TEXT NOT NULL DEFAULT 'project',
  subject_id            TEXT NOT NULL DEFAULT '',
  provider              TEXT NOT NULL DEFAULT '',
  model                 TEXT NOT NULL DEFAULT '',
  request_tokens        INTEGER NOT NULL DEFAULT 0,
  response_tokens       INTEGER NOT NULL DEFAULT 0,
  total_tokens          INTEGER NOT NULL DEFAULT 0,
  estimated_cost_cents  INTEGER NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT '',
  period                TEXT NOT NULL DEFAULT '',
  request_id            TEXT NOT NULL DEFAULT '',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_usage_period_project ON usage_events(project_id, period);
CREATE INDEX ix_usage_subject ON usage_events(project_id, subject_type, subject_id, period);
CREATE INDEX ix_usage_model ON usage_events(project_id, provider, model, period);

CREATE TABLE audit_logs (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT NOT NULL DEFAULT '',
  subject_type TEXT NOT NULL DEFAULT '',
  subject_id   TEXT NOT NULL DEFAULT '',
  action       TEXT NOT NULL DEFAULT '',
  provider     TEXT NOT NULL DEFAULT '',
  model        TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT '',
  message      TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_audit_project_time ON audit_logs(project_id, created_at DESC);
