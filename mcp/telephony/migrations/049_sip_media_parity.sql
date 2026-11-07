CREATE TABLE local_media_settings (
 project_id TEXT NOT NULL, route_id TEXT NOT NULL DEFAULT '', config_json TEXT NOT NULL,
 updated_at TEXT NOT NULL, PRIMARY KEY(project_id,route_id)
);
CREATE TABLE local_recording_files (
 recording_id TEXT PRIMARY KEY REFERENCES recordings(id), path TEXT NOT NULL, dropped_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE retired_sip_resources (
 id TEXT PRIMARY KEY, route_id TEXT NOT NULL, project_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL, provider TEXT NOT NULL, state_json TEXT NOT NULL,
 updated_at TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX retired_sip_project ON retired_sip_resources(project_id,route_id);
