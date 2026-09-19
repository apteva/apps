CREATE TABLE IF NOT EXISTS graphql_resolver_modules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  version INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  description TEXT NOT NULL DEFAULT '',
  inputs_json TEXT NOT NULL DEFAULT '{}',
  output_type TEXT NOT NULL,
  definition_json TEXT NOT NULL DEFAULT '{}',
  dependencies_json TEXT NOT NULL DEFAULT '[]',
  deterministic INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  published_at TEXT,
  UNIQUE(project_id, name, version)
);

CREATE INDEX IF NOT EXISTS idx_graphql_modules_project_name
  ON graphql_resolver_modules(project_id, name, version DESC);

CREATE INDEX IF NOT EXISTS idx_graphql_modules_published
  ON graphql_resolver_modules(project_id, status, name, version DESC);
