CREATE TABLE IF NOT EXISTS graphql_schemas (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT 'development',
  version INTEGER NOT NULL,
  sdl TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  hash TEXT NOT NULL,
  validation_errors TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  published_at TEXT,
  UNIQUE(project_id, environment, version)
);

CREATE INDEX IF NOT EXISTS idx_graphql_schemas_active
  ON graphql_schemas(project_id, environment, status, version DESC);

CREATE TABLE IF NOT EXISTS graphql_sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(project_id, name)
);

CREATE TABLE IF NOT EXISTS graphql_resolvers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  parent_type TEXT NOT NULL,
  field_name TEXT NOT NULL,
  source_id INTEGER NOT NULL,
  operation TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(project_id, parent_type, field_name),
  FOREIGN KEY(source_id) REFERENCES graphql_sources(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS graphql_request_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  operation_name TEXT NOT NULL DEFAULT '',
  operation_type TEXT NOT NULL DEFAULT '',
  status_code INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_graphql_logs_project_created
  ON graphql_request_logs(project_id, created_at DESC);
