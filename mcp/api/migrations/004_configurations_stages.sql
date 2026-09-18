-- Optional deployment-style configurations and stages. Legacy APIs continue to
-- use apis/api_routes directly; these tables are only consulted when a stage
-- hostname is configured.
CREATE TABLE IF NOT EXISTS api_configurations (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  api_id       INTEGER NOT NULL,
  version      INTEGER NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  description  TEXT NOT NULL DEFAULT '',
  allow_http   INTEGER NOT NULL DEFAULT 0,
  cors_json    TEXT NOT NULL DEFAULT '{}',
  auth_json    TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, api_id, version),
  FOREIGN KEY(api_id) REFERENCES apis(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS api_configuration_routes (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT NOT NULL,
  api_id         INTEGER NOT NULL,
  configuration_id INTEGER NOT NULL,
  method         TEXT NOT NULL,
  path_pattern   TEXT NOT NULL,
  target_kind    TEXT NOT NULL,
  target_ref     TEXT NOT NULL,
  target_path    TEXT NOT NULL DEFAULT '',
  events_json    TEXT NOT NULL DEFAULT '{}',
  auth_json      TEXT NOT NULL DEFAULT '{}',
  cors_json      TEXT NOT NULL DEFAULT '{}',
  timeout_ms     INTEGER NOT NULL DEFAULT 30000,
  enabled        INTEGER NOT NULL DEFAULT 1,
  priority       INTEGER NOT NULL DEFAULT 100,
  created_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(configuration_id, method, path_pattern),
  FOREIGN KEY(configuration_id) REFERENCES api_configurations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_api_configuration_routes_lookup
  ON api_configuration_routes(project_id, api_id, configuration_id, enabled, priority);

CREATE TABLE IF NOT EXISTS api_stages (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT NOT NULL,
  api_id         INTEGER NOT NULL,
  name           TEXT NOT NULL,
  configuration_id INTEGER NOT NULL,
  hostname       TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'active',
  cors_json      TEXT NOT NULL DEFAULT '',
  auth_json      TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, api_id, name),
  FOREIGN KEY(api_id) REFERENCES apis(id) ON DELETE CASCADE,
  FOREIGN KEY(configuration_id) REFERENCES api_configurations(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_api_stages_project_hostname
  ON api_stages(project_id, hostname)
  WHERE hostname <> '';

ALTER TABLE api_request_logs ADD COLUMN stage_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_request_logs ADD COLUMN configuration_id INTEGER NOT NULL DEFAULT 0;
