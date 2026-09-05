-- Durable lifecycle records. Existing file identities and bytes are preserved.
ALTER TABLE files ADD COLUMN share_generation INTEGER NOT NULL DEFAULT 0;

CREATE TABLE storage_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE blob_cleanup (
  object_key TEXT PRIMARY KEY,
  not_before INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE completed_uploads (
  upload_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  user_id INTEGER NOT NULL DEFAULT 0,
  folder TEXT NOT NULL,
  file_id INTEGER NOT NULL,
  was_existing INTEGER NOT NULL DEFAULT 0,
  completed_at INTEGER NOT NULL
);
CREATE INDEX ix_completed_uploads_expiry ON completed_uploads(completed_at);
CREATE INDEX ix_files_listing ON files(project_id, folder, name, id) WHERE deleted_at IS NULL;
CREATE INDEX ix_files_search ON files(project_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE TABLE upload_reservations(upload_id TEXT PRIMARY KEY,project_id TEXT NOT NULL,size_bytes INTEGER NOT NULL,expires_at INTEGER NOT NULL DEFAULT 0,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT INTO upload_reservations(upload_id,project_id,size_bytes,expires_at) SELECT upload_id,project_id,size_bytes,expires_at FROM pending_uploads;
