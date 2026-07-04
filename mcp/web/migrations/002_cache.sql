CREATE TABLE IF NOT EXISTS web_cache (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT NOT NULL,
  kind             TEXT NOT NULL,
  cache_key        TEXT NOT NULL,
  request_json     TEXT NOT NULL,
  response_json    TEXT NOT NULL,
  url              TEXT,
  title            TEXT,
  created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at       TIMESTAMP,
  last_accessed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  hit_count        INTEGER NOT NULL DEFAULT 0,
  UNIQUE(project_id, kind, cache_key)
);
CREATE INDEX IF NOT EXISTS ix_web_cache_project_kind_created
  ON web_cache(project_id, kind, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_web_cache_expires
  ON web_cache(expires_at);
