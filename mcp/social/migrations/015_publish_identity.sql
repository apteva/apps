-- Keep asynchronous provider operation IDs separate from public post IDs.
-- TikTok Direct Post returns v_pub_*/p_pub_* while processing and only
-- exposes the public numeric video ID later.

ALTER TABLE post_targets ADD COLUMN publish_operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN identity_resolve_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN identity_resolve_after TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_targets_identity_resolve
  ON post_targets(identity_resolve_after)
  WHERE publish_operation_id != '';
