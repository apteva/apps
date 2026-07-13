ALTER TABLE web_runs ADD COLUMN summary TEXT;

CREATE INDEX IF NOT EXISTS ix_web_cache_project_accessed
  ON web_cache(project_id, last_accessed_at DESC);
