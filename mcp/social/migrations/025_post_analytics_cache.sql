CREATE TABLE post_analytics_cache (
 project_id TEXT NOT NULL,
 target_id INTEGER NOT NULL,
 query_key TEXT NOT NULL,
 result TEXT NOT NULL,
 refreshed_at TEXT NOT NULL,
 PRIMARY KEY(project_id, target_id, query_key)
);
CREATE INDEX idx_post_analytics_cache_refresh ON post_analytics_cache(refreshed_at);

-- Earlier Pinterest account points describe the whole provider account, even
-- when attached to a board destination. Keep them away from board/widget totals.
UPDATE social_metric_points SET scope='provider_account'
WHERE platform='pinterest' AND scope='account';
