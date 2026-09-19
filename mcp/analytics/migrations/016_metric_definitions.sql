CREATE TABLE IF NOT EXISTS metric_definitions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  key TEXT NOT NULL,
  label TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  expression_json TEXT NOT NULL,
  unit TEXT NOT NULL DEFAULT 'number',
  format TEXT NOT NULL DEFAULT 'number',
  currency TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(project_id, key)
);
CREATE INDEX IF NOT EXISTS ix_metric_definitions_project ON metric_definitions(project_id, updated_at DESC);
