PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS eval_suites (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  environment_id TEXT NOT NULL DEFAULT '',
  judge_model TEXT NOT NULL DEFAULT '',
  continuous_targets_json TEXT NOT NULL DEFAULT '[]',
  schedule_minutes INTEGER NOT NULL DEFAULT 0,
  required_pass_rate REAL NOT NULL DEFAULT 1,
  enabled INTEGER NOT NULL DEFAULT 1,
  revision INTEGER NOT NULL DEFAULT 1,
  next_run_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS eval_cases (
  id TEXT PRIMARY KEY,
  suite_id TEXT NOT NULL REFERENCES eval_suites(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prompt TEXT NOT NULL,
  goals_json TEXT NOT NULL DEFAULT '[]',
  assertions_json TEXT NOT NULL DEFAULT '[]',
  environment_id TEXT NOT NULL DEFAULT '',
  weight REAL NOT NULL DEFAULT 1,
  timeout_seconds INTEGER NOT NULL DEFAULT 600,
  max_turns INTEGER NOT NULL DEFAULT 10,
  enabled INTEGER NOT NULL DEFAULT 1,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_eval_cases_suite ON eval_cases(suite_id, created_at);

CREATE TABLE IF NOT EXISTS eval_experiments (
  id TEXT PRIMARY KEY,
  suite_id TEXT NOT NULL,
  suite_revision INTEGER NOT NULL,
  name TEXT NOT NULL,
  trigger_type TEXT NOT NULL DEFAULT 'manual',
  status TEXT NOT NULL,
  targets_json TEXT NOT NULL,
  repetitions INTEGER NOT NULL DEFAULT 1,
  judge_model TEXT NOT NULL DEFAULT '',
  baseline_target INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_eval_experiments_created ON eval_experiments(created_at DESC);

CREATE TABLE IF NOT EXISTS eval_runs (
  id TEXT PRIMARY KEY,
  experiment_id TEXT NOT NULL REFERENCES eval_experiments(id) ON DELETE CASCADE,
  case_id TEXT NOT NULL,
  case_revision INTEGER NOT NULL,
  target_index INTEGER NOT NULL,
  repetition INTEGER NOT NULL,
  status TEXT NOT NULL,
  case_snapshot_json TEXT NOT NULL,
  target_snapshot_json TEXT NOT NULL,
  environment_run_id TEXT NOT NULL DEFAULT '',
  execution_json TEXT,
  assertions_json TEXT NOT NULL DEFAULT '[]',
  judge_json TEXT,
  correctness_score REAL,
  judge_score REAL,
  overall_score REAL,
  started_at TEXT,
  finished_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_eval_runs_experiment ON eval_runs(experiment_id, created_at);
CREATE INDEX IF NOT EXISTS idx_eval_runs_queue ON eval_runs(status, created_at);

CREATE TABLE IF NOT EXISTS eval_suggestions (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
  agent_id INTEGER NOT NULL,
  directive TEXT NOT NULL,
  expected_etag TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'proposed',
  created_at TEXT NOT NULL,
  applied_at TEXT
);

CREATE TABLE IF NOT EXISTS eval_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
