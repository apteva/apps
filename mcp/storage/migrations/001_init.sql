-- Storage v0.1.

CREATE TABLE files (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,

  -- User-visible filename (e.g. "summary.pdf"). Just the basename;
  -- folder lives separately so listing + moving are cheap.
  name         TEXT    NOT NULL,

  -- Slash-delimited path with leading + trailing slash (root = "/").
  -- Folders are virtual: a folder exists when a file references it.
  -- No separate folders table; same model as S3/R2.
  folder       TEXT    NOT NULL DEFAULT '/',

  -- Opaque key the storage layer uses internally. Today: uuid + .bin
  -- under <data>/blobs/<prefix>/<key>. Tomorrow it could be an S3
  -- object key — clients never see this.
  storage_key  TEXT    NOT NULL UNIQUE,

  content_type TEXT,
  size_bytes   INTEGER,
  sha256       TEXT,                          -- hex-encoded; ETag + dedup
  uploaded_by  TEXT,                          -- "agent:<id>" | "human:<id>" | "<integration>"
  source       TEXT,                          -- "chat-attachment" | "generated" | "imported" | …
  tags         TEXT    NOT NULL DEFAULT '[]', -- JSON array
  metadata     TEXT    NOT NULL DEFAULT '{}', -- arbitrary JSON, app-specific

  visibility   TEXT    NOT NULL DEFAULT 'private',  -- private | signed | public
  expires_at   TIMESTAMP,                     -- optional auto-delete deadline

  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at   TIMESTAMP                       -- soft delete; bytes stay on disk for v0.1
);

CREATE INDEX ix_files_proj    ON files(project_id, deleted_at);
CREATE INDEX ix_files_folder  ON files(project_id, folder, deleted_at);
CREATE INDEX ix_files_sha     ON files(project_id, sha256);
CREATE INDEX ix_files_name    ON files(project_id, name);
CREATE INDEX ix_files_updated ON files(project_id, updated_at DESC);
