PRAGMA foreign_keys = ON;

-- A scoring profile is the contract that turns metrics into a score. Sealed
-- like a pack: editing mints a new version rather than changing what existing
-- scores mean.
CREATE TABLE IF NOT EXISTS bench_profiles (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL DEFAULT 'draft',
  version         TEXT NOT NULL DEFAULT '',
  digest          TEXT NOT NULL DEFAULT '',
  source_id       TEXT NOT NULL DEFAULT '',
  builtin         INTEGER NOT NULL DEFAULT 0,
  on_failure      TEXT NOT NULL DEFAULT 'zero',
  components_json TEXT NOT NULL DEFAULT '[]',
  revision        INTEGER NOT NULL DEFAULT 1,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bench_profiles_digest ON bench_profiles(digest) WHERE digest <> '';
CREATE INDEX IF NOT EXISTS idx_bench_profiles_state ON bench_profiles(state, updated_at);

-- Which contract a pack is scored under. Pinned when the pack is sealed.
ALTER TABLE bench_packs ADD COLUMN profile_digest TEXT NOT NULL DEFAULT '';

-- Which contract a run was actually scored under. Backfilled at mount for rows
-- written before profiles existed — every one of them used verified-v1.
ALTER TABLE bench_runs ADD COLUMN scoring_profile_digest TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_bench_runs_profile ON bench_runs(pack_digest, scoring_profile_digest);
