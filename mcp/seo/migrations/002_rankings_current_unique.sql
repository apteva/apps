-- Make ranking rows idempotent for the current cache shape.
--
-- The app stores current page positions, not a SERP-history table. A repeated
-- domain refresh should update the same domain+keyword+URL row instead of
-- creating duplicates that make the panel show the same page twice.

DELETE FROM rankings
 WHERE id NOT IN (
       SELECT MAX(id)
         FROM rankings
        GROUP BY domain_id, keyword_id, location_id, provider, rank_url, device
 );

CREATE UNIQUE INDEX IF NOT EXISTS idx_rankings_current_unique
    ON rankings(domain_id, keyword_id, location_id, provider, rank_url, device);

CREATE TRIGGER IF NOT EXISTS trg_rankings_current_upsert
BEFORE INSERT ON rankings
WHEN EXISTS (
    SELECT 1
      FROM rankings
     WHERE domain_id = NEW.domain_id
       AND keyword_id = NEW.keyword_id
       AND location_id = NEW.location_id
       AND provider = NEW.provider
       AND rank_url = NEW.rank_url
       AND device = NEW.device
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
       AND device = NEW.device;
    SELECT RAISE(IGNORE);
END;
