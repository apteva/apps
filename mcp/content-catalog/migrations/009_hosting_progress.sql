ALTER TABLE hostings ADD COLUMN encode_progress REAL;
ALTER TABLE hostings ADD COLUMN provider_status INTEGER;
ALTER TABLE hostings ADD COLUMN provider_stage TEXT NOT NULL DEFAULT '';
ALTER TABLE hostings ADD COLUMN transcoding_messages TEXT NOT NULL DEFAULT '[]';
ALTER TABLE hostings ADD COLUMN next_check_at TEXT NOT NULL DEFAULT '';
ALTER TABLE hostings ADD COLUMN check_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hostings ADD COLUMN check_error TEXT NOT NULL DEFAULT '';
CREATE INDEX ix_hostings_pending ON hostings(status,next_check_at);

-- Only an explicit hosting action creates an intent. Never import intents
-- from files, folders, events, or historical hosting records.
CREATE TABLE hosting_intents (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, asset_id TEXT NOT NULL REFERENCES assets(id),
 session_id TEXT NOT NULL, storage_install_id INTEGER NOT NULL, storage_file_id TEXT NOT NULL,
 provider TEXT NOT NULL, connection_id INTEGER NOT NULL, library_id TEXT NOT NULL,
 collection_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('waiting_checksum','submitted','blocked','failed','cancelled')),
 checksum_status TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
 hosting_id TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0,
 next_check_at TEXT NOT NULL DEFAULT '', execution_token TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX ix_hosting_intent_active ON hosting_intents(project_id,asset_id) WHERE status='waiting_checksum';
CREATE INDEX ix_hosting_intents_pending ON hosting_intents(status,next_check_at);
