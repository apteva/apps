-- Media's transcript-audio proxy is uploaded through storage's
-- multipart HTTP route. Older storage versions ignored the multipart
-- source field and only accepted tags as a JSON array, so rows in the
-- hidden transcript-audio folder could land as source='human' with no
-- tags. Repair those internal rows on upgrade.

UPDATE files
   SET source = 'media-transcript-audio',
       updated_at = CURRENT_TIMESTAMP
 WHERE folder = '/.media/transcript-audio/'
   AND deleted_at IS NULL
   AND (source IS NULL OR source = '' OR source = 'human');

UPDATE files
   SET tags = '["internal","transcript-audio"]',
       updated_at = CURRENT_TIMESTAMP
 WHERE folder = '/.media/transcript-audio/'
   AND deleted_at IS NULL
   AND (tags IS NULL OR tags = '' OR tags = 'null' OR tags = '[]');
