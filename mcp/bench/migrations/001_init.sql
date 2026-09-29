PRAGMA foreign_keys = ON;

-- A pack is a benchmark definition. Drafts are editable; sealing copies the
-- draft into a new immutable row with a content digest. Sealed rows are never
-- mutated — editing a benchmark mints a new version instead.
CREATE TABLE IF NOT EXISTS bench_packs (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL DEFAULT 'draft',
  version         TEXT NOT NULL DEFAULT '',
  digest          TEXT NOT NULL DEFAULT '',
  scoring_version TEXT NOT NULL DEFAULT '',
  source_pack_id  TEXT NOT NULL DEFAULT '',
  scenarios_json  TEXT NOT NULL DEFAULT '[]',
  revision        INTEGER NOT NULL DEFAULT 1,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_bench_packs_state ON bench_packs(state, updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bench_packs_digest ON bench_packs(digest) WHERE digest <> '';

-- One Evals suite is materialized per sealed pack digest and reused by every
-- run of that digest. case_map_json maps the Evals case id back to our
-- scenario id so finished runs can be attributed.
CREATE TABLE IF NOT EXISTS bench_pack_suites (
  pack_digest   TEXT PRIMARY KEY,
  suite_id      TEXT NOT NULL,
  case_map_json TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS bench_runs (
  id              TEXT PRIMARY KEY,
  pack_id         TEXT NOT NULL,
  pack_name       TEXT NOT NULL DEFAULT '',
  pack_version    TEXT NOT NULL DEFAULT '',
  pack_digest     TEXT NOT NULL DEFAULT '',
  scoring_version TEXT NOT NULL DEFAULT '',
  name            TEXT NOT NULL DEFAULT '',
  targets_json    TEXT NOT NULL DEFAULT '[]',
  trials          INTEGER NOT NULL DEFAULT 1,
  suite_id        TEXT NOT NULL DEFAULT '',
  experiment_id   TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'queued',
  provenance_json TEXT NOT NULL DEFAULT '{}',
  summary_json    TEXT NOT NULL DEFAULT '{}',
  error           TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  started_at      TEXT,
  finished_at     TEXT
);
CREATE INDEX IF NOT EXISTS idx_bench_runs_status ON bench_runs(status, created_at);
CREATE INDEX IF NOT EXISTS idx_bench_runs_digest ON bench_runs(pack_digest, created_at);

CREATE TABLE IF NOT EXISTS bench_results (
  id             TEXT PRIMARY KEY,
  bench_run_id   TEXT NOT NULL REFERENCES bench_runs(id) ON DELETE CASCADE,
  scenario_id    TEXT NOT NULL,
  scenario_name  TEXT NOT NULL DEFAULT '',
  target_index   INTEGER NOT NULL DEFAULT 0,
  target_json    TEXT NOT NULL DEFAULT '{}',
  trial          INTEGER NOT NULL DEFAULT 1,
  eval_run_id    TEXT NOT NULL DEFAULT '',
  admission      TEXT NOT NULL DEFAULT 'verified',
  invalid_reason TEXT NOT NULL DEFAULT '',
  passed         INTEGER NOT NULL DEFAULT 0,
  score_json     TEXT NOT NULL DEFAULT '{}',
  metrics_json   TEXT NOT NULL DEFAULT '{}',
  error          TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_bench_results_run ON bench_results(bench_run_id, scenario_id);
CREATE INDEX IF NOT EXISTS idx_bench_results_admission ON bench_results(admission, scenario_id);

CREATE TABLE IF NOT EXISTS bench_baselines (
  id            TEXT PRIMARY KEY,
  pack_digest   TEXT NOT NULL,
  scenario_id   TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  target_json   TEXT NOT NULL DEFAULT '{}',
  score         REAL NOT NULL DEFAULT 0,
  pass_rate     REAL NOT NULL DEFAULT 0,
  metrics_json  TEXT NOT NULL DEFAULT '{}',
  source_run_id TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bench_baselines_key ON bench_baselines(pack_digest, scenario_id);
