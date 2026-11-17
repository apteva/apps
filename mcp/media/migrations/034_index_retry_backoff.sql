-- Failed files used to be retried on every indexer run (every 30 s by
-- default) with no delay, forever. Track consecutive failures and the
-- earliest next attempt so retries back off exponentially.
ALTER TABLE media ADD COLUMN index_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media ADD COLUMN index_retry_at TEXT;
