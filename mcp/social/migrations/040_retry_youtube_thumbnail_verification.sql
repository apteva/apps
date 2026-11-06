-- Resume verification only for retained receipts that timed out under the
-- old exact-URL check. The worker reads the saved video and receipt again.
UPDATE post_targets
SET youtube_thumbnail_state='verifying',
    youtube_thumbnail_due_at=0,
    youtube_thumbnail_attempts=0,
    youtube_thumbnail_error=''
WHERE youtube_thumbnail_state='failed'
  AND youtube_thumbnail_error='YouTube has not returned the applied custom thumbnail yet'
  AND youtube_thumbnail_receipt<>''
  AND youtube_thumbnail_video_id<>''
  AND youtube_thumbnail_video_id=platform_post_id
  AND status IN ('published','awaiting_audience_setup');
