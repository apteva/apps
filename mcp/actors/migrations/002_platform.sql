CREATE TABLE actors_versions (
  project_id TEXT NOT NULL,
  actor_id INTEGER NOT NULL,
  revision INTEGER NOT NULL,
  definition_json TEXT NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, actor_id, revision)
);
CREATE TABLE actors_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  actor_id INTEGER NOT NULL,
  revision INTEGER NOT NULL,
  operation TEXT NOT NULL,
  input_json TEXT NOT NULL,
  preset TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, name)
);
CREATE TABLE actors_dataset_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  run_id INTEGER NOT NULL REFERENCES actors_runs(id) ON DELETE CASCADE,
  item_json TEXT NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_actors_dataset_cursor ON actors_dataset_items(project_id, run_id, id);
CREATE TABLE actors_context_locks (
  project_id TEXT NOT NULL,
  context_id TEXT NOT NULL,
  run_id INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY(project_id, context_id)
);
