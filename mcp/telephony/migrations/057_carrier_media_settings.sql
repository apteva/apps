-- Installation-local carrier policy; credentials and conversation data are never stored.
CREATE TABLE IF NOT EXISTS carrier_media_settings (
 project_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 resource_type TEXT NOT NULL,
 resource_id TEXT NOT NULL,
 requested_json TEXT NOT NULL DEFAULT '',
 effective_json TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'unverified',
 verified_at TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,connection_id,resource_type,resource_id)
);
