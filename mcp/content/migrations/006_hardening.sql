ALTER TABLE media ADD COLUMN storage_file_id INTEGER;

CREATE INDEX IF NOT EXISTS media_storage_path_idx
  ON media (project_id, site_id, storage_path);

CREATE INDEX IF NOT EXISTS posts_scheduled_idx
  ON posts (project_id, status, scheduled_at)
  WHERE status = 'scheduled' AND deleted_at IS NULL;
