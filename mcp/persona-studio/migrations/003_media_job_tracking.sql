ALTER TABLE persona_assets ADD COLUMN media_job_id INTEGER;

CREATE INDEX IF NOT EXISTS idx_assets_media_job
  ON persona_assets(project_id, media_job_id, status);
