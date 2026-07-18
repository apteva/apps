-- 007 — keep background selection and cache pruning indexed as the universe
-- and user-entered chart key space grow.
CREATE INDEX IF NOT EXISTS idx_instrument_refreshed ON instrument(refreshed_at);
CREATE INDEX IF NOT EXISTS idx_cache_fetched_at ON cache(fetched_at);
