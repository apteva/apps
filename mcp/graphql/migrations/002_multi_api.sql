CREATE TABLE IF NOT EXISTS graphql_apis (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  base_path TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  default_api INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(project_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_graphql_apis_project ON graphql_apis(project_id, default_api DESC, slug);
