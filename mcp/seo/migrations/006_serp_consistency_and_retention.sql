-- seo v0.4.8 -- keep generic SERP reads fast and remove legacy rows that do
-- not satisfy the YouTube video-only contract.

CREATE TABLE IF NOT EXISTS ranking_observations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    domain_id     INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    location_id   INTEGER NOT NULL REFERENCES seo_locations(id),
    provider      TEXT    NOT NULL,
    device        TEXT    NOT NULL DEFAULT 'desktop',
    ts            INTEGER NOT NULL,
    observed_date TEXT    NOT NULL,
    result_count  INTEGER NOT NULL DEFAULT 0,
    UNIQUE(domain_id, location_id, provider, device, observed_date)
);

INSERT OR IGNORE INTO ranking_observations
    (domain_id, location_id, provider, device, ts, observed_date, result_count)
SELECT domain_id, location_id, provider, device, MAX(ts), observed_date, COUNT(*)
  FROM rankings
 WHERE observed_date IS NOT NULL AND observed_date <> ''
 GROUP BY domain_id, location_id, provider, device, observed_date;

CREATE INDEX IF NOT EXISTS idx_ranking_observations_latest
    ON ranking_observations(domain_id, location_id, provider, device, observed_date DESC);

DELETE FROM search_serp_results
 WHERE snapshot_id IN (
       SELECT id FROM search_serp_snapshots WHERE search_engine = 'youtube'
 )
   AND (
       result_type <> 'video'
       OR lower(url) LIKE '%youtube.com/@%'
       OR lower(url) LIKE '%youtube.com/channel/%'
       OR lower(url) LIKE '%youtube.com/c/%'
       OR lower(url) LIKE '%youtube.com/user/%'
       OR lower(url) LIKE '%youtube.com/playlist%'
   );

DELETE FROM search_serp_snapshots
 WHERE id IN (
       SELECT id
         FROM (
              SELECT id,
                     ROW_NUMBER() OVER (
                       PARTITION BY project_id, search_engine, keyword_text, location_id
                       ORDER BY ts DESC, id DESC
                     ) AS snapshot_rank
                FROM search_serp_snapshots
         ) ranked
        WHERE snapshot_rank > 30
 );

CREATE INDEX IF NOT EXISTS idx_search_serp_snapshots_keyword
    ON search_serp_snapshots(project_id, keyword_id, ts DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_search_serp_snapshots_latest
    ON search_serp_snapshots(project_id, search_engine, keyword_text, location_id, ts DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_rankings_current_observation
    ON rankings(domain_id, location_id, provider, device, observed_date DESC, rank);
