-- Runners apply every migration on each boot, so everything here must be
-- repeatable: no ALTER TABLE. Per-session extras live in a side table.
CREATE TABLE IF NOT EXISTS mobile_session_meta (
  session_id TEXT PRIMARY KEY,
  caller_ref TEXT NOT NULL DEFAULT '',
  ended_at INTEGER,
  end_reason TEXT NOT NULL DEFAULT '',
  stop_reason TEXT NOT NULL DEFAULT '',
  stopped_by TEXT NOT NULL DEFAULT '',
  paused_at INTEGER
);
-- A paused session (phone dropped off USB) still owns its phone.
DROP INDEX IF EXISTS mobile_device_lease;
CREATE UNIQUE INDEX IF NOT EXISTS mobile_device_lease_v2 ON mobile_sessions(device_id)
  WHERE status IN ('opening','active','paused','cleanup_failed');
CREATE TABLE IF NOT EXISTS mobile_baselines (
  project_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  captured_at INTEGER NOT NULL,
  baseline_json TEXT NOT NULL,
  PRIMARY KEY (project_id, device_id)
);
