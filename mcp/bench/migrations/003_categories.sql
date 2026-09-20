PRAGMA foreign_keys = ON;

-- Categories organize multiple immutable packs into a comparable benchmark
-- family. Runs snapshot the category so history remains searchable even if a
-- draft is later edited. Results snapshot scenario tags for the same reason.
ALTER TABLE bench_packs ADD COLUMN category TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_bench_packs_category ON bench_packs(category, state, updated_at);

ALTER TABLE bench_runs ADD COLUMN pack_category TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_bench_runs_category ON bench_runs(pack_category, scoring_profile_digest, created_at);

ALTER TABLE bench_results ADD COLUMN scenario_tags_json TEXT NOT NULL DEFAULT '[]';
