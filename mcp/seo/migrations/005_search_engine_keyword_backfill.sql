-- seo v0.4.7 -- backfill the uniform search-engine keyword model for
-- installs that created keywords before search_engine-aware SERP support.

ALTER TABLE keywords ADD COLUMN search_engine TEXT NOT NULL DEFAULT 'google';

UPDATE keywords
   SET search_engine = COALESCE((
       SELECT l.search_engine
         FROM seo_locations l
        WHERE l.id = keywords.location_id
   ), 'google');

CREATE INDEX IF NOT EXISTS idx_keywords_scope_engine
    ON keywords(project_id, search_engine, country_iso);

INSERT OR IGNORE INTO keywords
    (project_id, search_engine, text, location_id, country_iso, language_iso)
SELECT DISTINCT s.project_id,
       s.search_engine,
       lower(trim(s.keyword_text)),
       s.location_id,
       COALESCE(upper(l.country_iso), ''),
       lower(COALESCE(l.language_code, 'en'))
  FROM search_serp_snapshots s
  JOIN seo_locations l ON l.id = s.location_id
 WHERE s.search_engine IN ('google', 'youtube')
   AND trim(s.keyword_text) <> ''
   AND s.location_id IS NOT NULL;

UPDATE search_serp_snapshots
   SET keyword_id = (
       SELECT k.id
         FROM keywords k
        WHERE k.project_id = search_serp_snapshots.project_id
          AND k.text = lower(trim(search_serp_snapshots.keyword_text))
          AND k.location_id = search_serp_snapshots.location_id
        LIMIT 1
   )
 WHERE keyword_id IS NULL
   AND search_engine IN ('google', 'youtube')
   AND trim(keyword_text) <> ''
   AND location_id IS NOT NULL;

DELETE FROM search_serp_results
 WHERE id IN (
       SELECT r.id
         FROM search_serp_results r
         JOIN search_serp_snapshots s ON s.id = r.snapshot_id
        WHERE s.search_engine = 'youtube'
          AND (
              r.result_type IN ('youtube_channel', 'channel', 'youtube_playlist', 'playlist')
              OR r.url LIKE '%/channel/%'
              OR r.url LIKE '%/playlist%'
          )
   );

UPDATE search_serp_results
   SET result_type = 'video'
 WHERE id IN (
       SELECT r.id
         FROM search_serp_results r
         JOIN search_serp_snapshots s ON s.id = r.snapshot_id
        WHERE s.search_engine = 'youtube'
          AND r.result_type IN ('youtube_video', '')
   );
