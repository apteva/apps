ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_state TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_video_id TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_file_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_due_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_receipt TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN youtube_thumbnail_error TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_post_targets_youtube_thumbnail_due ON post_targets(youtube_thumbnail_due_at)
WHERE youtube_thumbnail_state IN ('pending','verifying');
