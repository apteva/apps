CREATE TABLE IF NOT EXISTS embeds (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  token              TEXT NOT NULL UNIQUE,
  project_id         TEXT NOT NULL,
  storage_file_id    INTEGER NOT NULL,
  storage_project_id TEXT NOT NULL DEFAULT '',
  title              TEXT NOT NULL DEFAULT '',
  name               TEXT NOT NULL DEFAULT '',
  content_type       TEXT NOT NULL DEFAULT '',
  size_bytes         INTEGER NOT NULL DEFAULT 0,
  width              INTEGER NOT NULL DEFAULT 0,
  height             INTEGER NOT NULL DEFAULT 0,
  status             TEXT NOT NULL DEFAULT 'active',
  created_at         TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_embeds_project_created
  ON embeds(project_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_embeds_storage
  ON embeds(project_id, storage_file_id);
