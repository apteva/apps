CREATE TABLE sip_audio_profiles (
 project_id TEXT NOT NULL,
 scope_key TEXT NOT NULL,
 revision INTEGER NOT NULL,
 config_json TEXT NOT NULL,
 expires_at TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,scope_key)
);
