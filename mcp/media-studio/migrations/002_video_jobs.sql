-- video_jobs — async video generation queue tracking.
--
-- Venice's /video/queue is fire-and-poll; we submit, get a queue_id,
-- then a worker polls /video/retrieve every 15s until completion.
-- Once retrieved, the bytes flow into storage and a generations row
-- lands; this table just tracks the in-flight handoff.

CREATE TABLE video_jobs (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id       TEXT    NOT NULL,
  queue_id         TEXT    NOT NULL,            -- Venice's job handle
  provider         TEXT    NOT NULL,            -- app_slug (venice-ai today)
  model            TEXT    NOT NULL,            -- echo of the model used
  prompt           TEXT    NOT NULL,
  source_image_ref TEXT    DEFAULT '',          -- image-to-video lineage (storage:N / URL)
  request_json     TEXT    NOT NULL DEFAULT '{}', -- original args for replay/debugging
  status           TEXT    NOT NULL DEFAULT 'queued', -- queued | polling | complete | failed
  error            TEXT    DEFAULT '',
  result_storage_id INTEGER DEFAULT 0,          -- populated when bytes land in storage
  generation_id    INTEGER DEFAULT 0,           -- populated when the generations row is written
  attempts         INTEGER NOT NULL DEFAULT 0,  -- worker poll count
  last_poll_at     TIMESTAMP,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Worker scans this index on every tick — narrow it to the in-flight rows.
CREATE INDEX idx_video_jobs_status_pending
  ON video_jobs(status, last_poll_at)
  WHERE status IN ('queued', 'polling');

CREATE INDEX idx_video_jobs_project
  ON video_jobs(project_id, id DESC);
