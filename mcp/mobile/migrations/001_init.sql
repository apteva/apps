CREATE TABLE IF NOT EXISTS mobile_sessions (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  device_json TEXT NOT NULL,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  provider_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS mobile_device_lease ON mobile_sessions(device_id)
  WHERE status IN ('opening','active','cleanup_failed');
CREATE INDEX IF NOT EXISTS mobile_sessions_project ON mobile_sessions(project_id, created_at);
CREATE TABLE IF NOT EXISTS mobile_runs (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  name TEXT NOT NULL,
  build_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  failure_kind TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  finished_at INTEGER,
  error TEXT NOT NULL DEFAULT '',
  results_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS mobile_runs_project ON mobile_runs(project_id, created_at);
CREATE TABLE IF NOT EXISTS mobile_artifacts (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mobile_notifications (
  run_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  notified INTEGER NOT NULL DEFAULT 0
);
