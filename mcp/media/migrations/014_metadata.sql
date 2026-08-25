-- Arbitrary workflow metadata owned by Media consumers. The probe/indexer
-- deliberately never writes these columns, so re-indexing cannot erase
-- coordination state added by agents or other apps.
ALTER TABLE media ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'
  CHECK (json_valid(metadata) AND json_type(metadata) = 'object');

-- Monotonic optimistic-concurrency revision. Existing rows start at zero;
-- every successful media_patch_metadata call increments it exactly once.
ALTER TABLE media ADD COLUMN metadata_version INTEGER NOT NULL DEFAULT 0
  CHECK (metadata_version >= 0);
