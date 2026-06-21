-- Preserve ranking history as one observation per day.
--
-- v0.3.5 made rankings a current-row cache to prevent duplicate page/keyword
-- rows. This migration keeps that dedupe property for repeated refreshes on
-- the same day, while allowing tomorrow's refresh to insert a new observation.

ALTER TABLE rankings ADD COLUMN observed_date TEXT;

UPDATE rankings
   SET observed_date = date(ts, 'unixepoch')
 WHERE observed_date IS NULL OR observed_date = '';

DROP TRIGGER IF EXISTS trg_rankings_current_upsert;
DROP INDEX IF EXISTS idx_rankings_current_unique;

DELETE FROM rankings
 WHERE id NOT IN (
       SELECT MAX(id)
         FROM rankings
        GROUP BY domain_id, keyword_id, location_id, provider, rank_url, device, observed_date
 );

CREATE UNIQUE INDEX IF NOT EXISTS idx_rankings_daily_unique
    ON rankings(domain_id, keyword_id, location_id, provider, rank_url, device, observed_date);

CREATE INDEX IF NOT EXISTS idx_rankings_observed_date
    ON rankings(observed_date DESC);

CREATE TRIGGER IF NOT EXISTS trg_rankings_daily_upsert
BEFORE INSERT ON rankings
WHEN NEW.observed_date IS NOT NULL
 AND EXISTS (
    SELECT 1
      FROM rankings
     WHERE domain_id = NEW.domain_id
       AND keyword_id = NEW.keyword_id
       AND location_id = NEW.location_id
       AND provider = NEW.provider
       AND rank_url = NEW.rank_url
       AND device = NEW.device
       AND observed_date = NEW.observed_date
)
BEGIN
    UPDATE rankings
       SET ts = NEW.ts,
           rank = NEW.rank,
           serp_features_json = NEW.serp_features_json
     WHERE domain_id = NEW.domain_id
       AND keyword_id = NEW.keyword_id
       AND location_id = NEW.location_id
       AND provider = NEW.provider
       AND rank_url = NEW.rank_url
       AND device = NEW.device
       AND observed_date = NEW.observed_date;
    SELECT RAISE(IGNORE);
END;
