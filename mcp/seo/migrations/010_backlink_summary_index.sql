-- Cover and order daily backlink aggregation without scanning URL/raw payloads
-- or building a temporary sort for every summary request.
CREATE INDEX IF NOT EXISTS idx_backlinks_summary
    ON backlinks(domain_id, provider, first_seen / 86400, last_seen / 86400,
                 is_lost, first_seen, last_seen);
