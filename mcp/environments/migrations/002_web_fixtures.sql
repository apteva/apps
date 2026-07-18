CREATE TABLE IF NOT EXISTS environment_web_fixtures (
  run_id TEXT NOT NULL,
  fixture_id TEXT NOT NULL,
  pack TEXT NOT NULL,
  pack_version TEXT NOT NULL,
  scenario TEXT NOT NULL,
  token TEXT NOT NULL UNIQUE,
  seed_json TEXT NOT NULL,
  initial_state_json TEXT NOT NULL,
  state_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'running',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (run_id, fixture_id)
);

CREATE TABLE IF NOT EXISTS environment_web_fixture_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL,
  fixture_id TEXT NOT NULL,
  type TEXT NOT NULL,
  data_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_environment_web_fixture_events
  ON environment_web_fixture_events(run_id, fixture_id, id);

CREATE TABLE IF NOT EXISTS environment_web_fixture_snapshots (
  snapshot_id TEXT NOT NULL,
  fixture_id TEXT NOT NULL,
  pack TEXT NOT NULL,
  pack_version TEXT NOT NULL,
  scenario TEXT NOT NULL,
  seed_json TEXT NOT NULL,
  state_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (snapshot_id, fixture_id)
);
