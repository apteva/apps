CREATE TABLE IF NOT EXISTS actors_effect_guards (
  project_id TEXT NOT NULL,
  actor_id INTEGER NOT NULL,
  operation TEXT NOT NULL,
  once_key TEXT NOT NULL,
  run_id INTEGER NOT NULL,
  expected_effect TEXT NOT NULL,
  state TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  PRIMARY KEY (project_id, actor_id, operation, once_key)
);
