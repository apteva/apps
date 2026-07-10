-- Persist the exact provider connection used to create async work.
-- This keeps polling stable when bindings change and lets global installs
-- process each project's jobs with the correct credentials.

ALTER TABLE video_jobs ADD COLUMN connection_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE avatar_create_jobs ADD COLUMN connection_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_video_jobs_project_pending
  ON video_jobs(project_id, status, id)
  WHERE status IN ('queued', 'polling', 'finalizing');

CREATE INDEX idx_avatar_create_jobs_project_pending
  ON avatar_create_jobs(project_id, status, id)
  WHERE status IN ('queued', 'training');
