CREATE INDEX IF NOT EXISTS idx_social_metric_points_target_refresh
ON social_metric_points (
  project_id,
  scope,
  post_target_id,
  metric,
  point_time DESC
);
