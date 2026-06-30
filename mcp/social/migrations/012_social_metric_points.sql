CREATE TABLE IF NOT EXISTS social_metric_points (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  profile_id INTEGER DEFAULT 0,
  social_account_id INTEGER NOT NULL,
  post_id INTEGER DEFAULT 0,
  post_target_id INTEGER DEFAULT 0,
  platform TEXT NOT NULL,
  scope TEXT NOT NULL,
  metric TEXT NOT NULL,
  period TEXT NOT NULL DEFAULT 'snapshot',
  point_time TEXT NOT NULL,
  value INTEGER NOT NULL,
  source TEXT NOT NULL DEFAULT 'social',
  status TEXT NOT NULL DEFAULT 'ok',
  note TEXT DEFAULT '',
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_social_metric_points_unique
ON social_metric_points (
  project_id,
  scope,
  social_account_id,
  post_target_id,
  metric,
  period,
  point_time,
  source
);

CREATE INDEX IF NOT EXISTS idx_social_metric_points_account_time
ON social_metric_points (project_id, social_account_id, metric, point_time);

CREATE INDEX IF NOT EXISTS idx_social_metric_points_profile_time
ON social_metric_points (project_id, profile_id, metric, point_time);

CREATE INDEX IF NOT EXISTS idx_social_metric_points_post_time
ON social_metric_points (project_id, post_id, metric, point_time);
