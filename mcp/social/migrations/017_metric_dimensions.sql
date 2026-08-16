ALTER TABLE social_metric_points
ADD COLUMN dimensions_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE social_metric_points
ADD COLUMN dimensions_key TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_social_metric_points_unique;

CREATE UNIQUE INDEX IF NOT EXISTS idx_social_metric_points_unique
ON social_metric_points (
  project_id,
  scope,
  social_account_id,
  post_target_id,
  metric,
  period,
  point_time,
  source,
  dimensions_key
);

CREATE INDEX IF NOT EXISTS idx_social_metric_points_dimensions
ON social_metric_points (
  project_id,
  social_account_id,
  scope,
  period,
  dimensions_key,
  point_time
);
