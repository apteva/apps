-- Import support: distinguish posts pulled from upstream from posts
-- authored locally, and dedup re-imports.
--
-- imported_at:        non-null timestamp = imported from a platform; null = locally authored
-- external_media_urls: JSON array of upstream media URLs we DON'T own
--                      (FB picture, IG media_url). Avoids round-tripping
--                      bytes through our storage app for read-only views.
ALTER TABLE posts ADD COLUMN imported_at TIMESTAMP;
ALTER TABLE posts ADD COLUMN external_media_urls TEXT;

-- Free dedupe for re-imports: INSERT OR IGNORE on post_targets keyed
-- by (social_account_id, platform_post_id) silently skips rows we
-- already have. Partial index — rows with NULL platform_post_id (a
-- pending or pre-publish target) don't participate.
CREATE UNIQUE INDEX IF NOT EXISTS idx_targets_dedupe
  ON post_targets(social_account_id, platform_post_id)
  WHERE platform_post_id IS NOT NULL AND platform_post_id != '';
