-- Thumbnail impressions and click-through rate come from bulk daily
-- reach reports (YouTube Reporting API), not live queries. One reporting
-- job per account, then one row per day and video from its reports.
CREATE TABLE IF NOT EXISTS reach_jobs (
  social_account_id   INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  platform            TEXT    NOT NULL,
  report_type_id      TEXT    NOT NULL DEFAULT '',
  job_id              TEXT    NOT NULL DEFAULT '',
  job_created_at      TEXT,
  reports_cursor      TEXT    NOT NULL DEFAULT '', -- createTime of the newest report read
  state               TEXT    NOT NULL DEFAULT 'not_started', -- not_started | collecting | ok | needs_platform_update | needs_reconnect | unavailable | error
  last_error          TEXT,
  last_checked_at     TEXT
);

CREATE TABLE IF NOT EXISTS reach_daily (
  social_account_id   INTEGER NOT NULL,
  day                 TEXT    NOT NULL, -- YYYY-MM-DD
  video_id            TEXT    NOT NULL,
  impressions         INTEGER NOT NULL DEFAULT 0,
  clicks              REAL    NOT NULL DEFAULT 0, -- impressions x click-through rate
  report_id           TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (social_account_id, day, video_id)
);
CREATE INDEX IF NOT EXISTS reach_daily_video ON reach_daily(social_account_id, video_id);
