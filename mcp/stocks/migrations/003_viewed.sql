-- 003 — tiered freshness. Track when a symbol was last opened (get /
-- dividends) so the warming worker can keep recently-viewed stocks fresh
-- on a short cycle while the cold tail of the S&P 1500 trickles. NULL =
-- never viewed.
ALTER TABLE instrument ADD COLUMN viewed_at INTEGER;
CREATE INDEX IF NOT EXISTS idx_instrument_viewed ON instrument(viewed_at);
