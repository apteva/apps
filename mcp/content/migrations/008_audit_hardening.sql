ALTER TABLE posts ADD COLUMN edit_version INTEGER NOT NULL DEFAULT 1;
-- Normalize schedules written by releases that stored RFC3339 verbatim.
UPDATE posts SET scheduled_at=strftime('%Y-%m-%d %H:%M:%S', scheduled_at)
WHERE scheduled_at IS NOT NULL AND strftime('%Y-%m-%d %H:%M:%S', scheduled_at) IS NOT NULL;
CREATE TABLE content_maintenance (key TEXT PRIMARY KEY);
