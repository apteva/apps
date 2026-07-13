CREATE TABLE IF NOT EXISTS environment_definitions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  desired_state TEXT NOT NULL DEFAULT 'stopped',
  spec_version INTEGER NOT NULL DEFAULT 1,
  spec_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS environment_runs (
  id TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL DEFAULT '',
  runtime_id TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL DEFAULT 'interactive',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  stopped_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_environment_runs_environment ON environment_runs(environment_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_environment_runs_status ON environment_runs(status);

CREATE TABLE IF NOT EXISTS environment_snapshots (
  id TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS environment_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
