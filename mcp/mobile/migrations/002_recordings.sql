CREATE TABLE IF NOT EXISTS mobile_recordings (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  parent_id TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  device_id TEXT NOT NULL DEFAULT '',
  platform TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL DEFAULT '',
  preset TEXT NOT NULL DEFAULT '',
  audio INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  file_name TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  fps REAL NOT NULL DEFAULT 0,
  video_codec TEXT NOT NULL DEFAULT '',
  audio_codec TEXT NOT NULL DEFAULT '',
  markers_json TEXT NOT NULL DEFAULT '[]',
  checks_json TEXT NOT NULL DEFAULT '[]',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  started_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER
);
CREATE INDEX IF NOT EXISTS mobile_recordings_project ON mobile_recordings(project_id, created_at);
CREATE INDEX IF NOT EXISTS mobile_recordings_session ON mobile_recordings(session_id, status);
CREATE INDEX IF NOT EXISTS mobile_recordings_run ON mobile_recordings(run_id);
CREATE TABLE IF NOT EXISTS mobile_recording_uploads (
  recording_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  status TEXT NOT NULL,
  storage_file_id INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
