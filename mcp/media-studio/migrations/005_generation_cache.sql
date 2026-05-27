-- Stable cache keys let callers such as Composer ask Media Studio to
-- reuse identical generated assets instead of billing the provider again.

ALTER TABLE generations ADD COLUMN cache_key TEXT NOT NULL DEFAULT '';
ALTER TABLE video_jobs  ADD COLUMN cache_key TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_generations_cache
  ON generations(project_id, kind, cache_key, id DESC)
  WHERE cache_key <> '';

CREATE INDEX idx_video_jobs_cache_pending
  ON video_jobs(project_id, kind, cache_key, status, id DESC)
  WHERE cache_key <> '' AND status IN ('queued', 'polling');
