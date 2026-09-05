ALTER TABLE posts ADD COLUMN schedule_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN delivery_revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE post_targets ADD COLUMN delete_status TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN delete_error TEXT NOT NULL DEFAULT '';
CREATE TABLE account_metric_cache (
 project_id TEXT NOT NULL, social_account_id INTEGER NOT NULL, query_key TEXT NOT NULL,
 result TEXT NOT NULL, refreshed_at TEXT NOT NULL, PRIMARY KEY(project_id,social_account_id,query_key)
);
UPDATE profiles SET is_default=0 WHERE is_default=1 AND id NOT IN (SELECT MIN(id) FROM profiles WHERE is_default=1 GROUP BY project_id);
CREATE UNIQUE INDEX idx_profiles_one_default ON profiles(project_id) WHERE is_default=1;
CREATE INDEX idx_social_breakdown_latest ON social_metric_points(project_id,social_account_id,scope,period,source,status,point_time DESC);
CREATE INDEX idx_inbox_conversation ON inbox_items(project_id,social_account_id,kind,external_post_id,occurred_at,id);

ALTER TABLE pending_accounts ADD COLUMN callback_nonce TEXT NOT NULL DEFAULT '';
