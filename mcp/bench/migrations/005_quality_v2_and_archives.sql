PRAGMA foreign_keys = ON;

-- Archival is display metadata, not part of the immutable benchmark digest.
-- It hides superseded packs from default discovery without deleting definitions,
-- runs, evidence, or leaderboards.
ALTER TABLE bench_packs ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bench_packs ADD COLUMN superseded_by TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_bench_packs_archive ON bench_packs(archived, state, updated_at);
