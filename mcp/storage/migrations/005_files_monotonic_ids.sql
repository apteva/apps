-- File IDs are durable cross-app identities. SQLite's plain
-- `INTEGER PRIMARY KEY` may reuse the highest deleted ROWID; Media persists
-- those IDs for thumbnails/keyframes, so reuse can make a stale reference
-- resolve to an unrelated user file. Rebuild the table with AUTOINCREMENT so
-- every future ID is greater than every ID allocated after this migration.

CREATE TABLE files_monotonic (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  folder       TEXT    NOT NULL DEFAULT '/',
  storage_key  TEXT    NOT NULL UNIQUE,
  content_type TEXT,
  size_bytes   INTEGER,
  sha256       TEXT,
  uploaded_by  TEXT,
  source       TEXT,
  tags         TEXT    NOT NULL DEFAULT '[]',
  metadata     TEXT    NOT NULL DEFAULT '{}',
  visibility   TEXT    NOT NULL DEFAULT 'private',
  expires_at   TIMESTAMP,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at   TIMESTAMP
);

INSERT INTO files_monotonic (
  id, project_id, name, folder, storage_key, content_type, size_bytes,
  sha256, uploaded_by, source, tags, metadata, visibility, expires_at,
  created_at, updated_at, deleted_at
)
SELECT
  id, project_id, name, folder, storage_key, content_type, size_bytes,
  sha256, uploaded_by, source, tags, metadata, visibility, expires_at,
  created_at, updated_at, deleted_at
FROM files;

DROP TABLE files;
ALTER TABLE files_monotonic RENAME TO files;

CREATE INDEX ix_files_proj    ON files(project_id, deleted_at);
CREATE INDEX ix_files_folder  ON files(project_id, folder, deleted_at);
CREATE INDEX ix_files_sha     ON files(project_id, sha256);
CREATE INDEX ix_files_name    ON files(project_id, name);
CREATE INDEX ix_files_updated ON files(project_id, updated_at DESC);
