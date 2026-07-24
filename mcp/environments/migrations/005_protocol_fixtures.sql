CREATE TABLE IF NOT EXISTS environment_protocol_fixtures (
  run_id TEXT NOT NULL,
  fixture_id TEXT NOT NULL,
  pack TEXT NOT NULL,
  pack_version TEXT NOT NULL,
  protocol TEXT NOT NULL,
  target_app TEXT NOT NULL,
  status TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (run_id, fixture_id)
);

CREATE TABLE IF NOT EXISTS environment_protocol_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL,
  fixture_id TEXT NOT NULL,
  call_id TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL,
  direction TEXT NOT NULL DEFAULT '',
  data_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_environment_protocol_events_fixture
ON environment_protocol_events(run_id, fixture_id, id);
