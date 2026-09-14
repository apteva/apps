-- Durable indexing-attempt bookkeeping.
--
-- probe_status remains the public probe state. These columns record the
-- worker's current claim and last diagnostic so a crashed sidecar can be
-- diagnosed and reclaimed without leaving an opaque pending row forever.
ALTER TABLE media ADD COLUMN index_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media ADD COLUMN index_claimed_at TIMESTAMP;
ALTER TABLE media ADD COLUMN index_last_error TEXT NOT NULL DEFAULT '';
