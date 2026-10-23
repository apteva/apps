CREATE TABLE agent_audio_profiles (
  project_id TEXT NOT NULL,
  scope_key TEXT NOT NULL,
  config_json TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project_id,scope_key)
);
CREATE TABLE agent_audio_call_profiles (
  call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  config_json TEXT NOT NULL
);
