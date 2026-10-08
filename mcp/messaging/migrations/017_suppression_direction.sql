-- Legacy blocks retain BOTH directions. New operator opt-outs can stop
-- outbound delivery without losing future incoming replies. No rows deleted.
ALTER TABLE suppressions ADD COLUMN direction TEXT NOT NULL DEFAULT 'both'
  CHECK (direction IN ('both', 'outbound'));
