-- avatar_create_jobs tracks reusable avatar/replica creation. This is
-- separate from video_jobs because completion produces an avatar ID,
-- not a media generation row.

CREATE TABLE avatar_create_jobs (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id         TEXT    NOT NULL,
  provider           TEXT    NOT NULL,
  source_type        TEXT    NOT NULL, -- photo | prompt | video
  name               TEXT    NOT NULL,
  provider_job_id    TEXT    DEFAULT '',
  provider_avatar_id TEXT    DEFAULT '',
  provider_group_id  TEXT    DEFAULT '',
  source_ref         TEXT    DEFAULT '',
  consent_ref        TEXT    DEFAULT '',
  request_json       TEXT    NOT NULL DEFAULT '{}',
  status             TEXT    NOT NULL DEFAULT 'queued', -- queued | training | completed | failed
  error              TEXT    DEFAULT '',
  attempts           INTEGER NOT NULL DEFAULT 0,
  last_poll_at       TIMESTAMP,
  created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_avatar_create_jobs_status_pending
  ON avatar_create_jobs(status, last_poll_at)
  WHERE status IN ('queued', 'training');

CREATE INDEX idx_avatar_create_jobs_project
  ON avatar_create_jobs(project_id, id DESC);
