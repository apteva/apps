-- Trading v0.9.0 - codified portfolio universes and durable strategy scorecards.

CREATE TABLE portfolio_universe_policies (
  portfolio_id          INTEGER PRIMARY KEY REFERENCES portfolios(id) ON DELETE CASCADE,
  project_id            TEXT    NOT NULL,
  selection_mode        TEXT    NOT NULL DEFAULT 'all_allowed_classes'
    CHECK (selection_mode IN ('all_allowed_classes','symbol_allowlist','reference_universe')),
  include_symbols       TEXT    NOT NULL DEFAULT '[]',
  exclude_symbols       TEXT    NOT NULL DEFAULT '[]',
  reference_universe_id TEXT    NOT NULL DEFAULT '',
  require_active_listing INTEGER NOT NULL DEFAULT 0 CHECK (require_active_listing IN (0,1)),
  enforcement_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (enforcement_enabled IN (0,1)),
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_portfolio_universe_project
  ON portfolio_universe_policies(project_id, selection_mode);

CREATE TABLE strategy_scorecard_policies (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  portfolio_id          INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  strategy_id           INTEGER NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,
  criteria_json         TEXT    NOT NULL DEFAULT '[]',
  min_completed_runs    INTEGER NOT NULL DEFAULT 1 CHECK (min_completed_runs > 0),
  require_out_of_sample INTEGER NOT NULL DEFAULT 1 CHECK (require_out_of_sample IN (0,1)),
  enforcement_enabled   INTEGER NOT NULL DEFAULT 0 CHECK (enforcement_enabled IN (0,1)),
  promotion_stage       TEXT    NOT NULL DEFAULT 'research'
    CHECK (promotion_stage IN ('research','paper_candidate','paper','live_candidate','live','suspended')),
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, portfolio_id, strategy_id)
);
CREATE INDEX ix_strategy_scorecard_policy
  ON strategy_scorecard_policies(project_id, strategy_id, promotion_stage);

CREATE TABLE strategy_scorecard_evaluations (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT    NOT NULL,
  portfolio_id      INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  strategy_id       INTEGER NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,
  strategy_version  INTEGER NOT NULL,
  backtest_run_id   INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
  evaluation_scope  TEXT    NOT NULL,
  passed            INTEGER NOT NULL CHECK (passed IN (0,1)),
  verdict           TEXT    NOT NULL,
  metrics_json      TEXT    NOT NULL,
  checks_json       TEXT    NOT NULL,
  policy_json       TEXT    NOT NULL,
  policy_hash       TEXT    NOT NULL,
  dataset_sha256    TEXT    NOT NULL DEFAULT '',
  evaluated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_strategy_scorecard_evaluations_latest
  ON strategy_scorecard_evaluations(project_id, portfolio_id, strategy_id, id DESC);
CREATE INDEX ix_strategy_scorecard_evaluations_run
  ON strategy_scorecard_evaluations(backtest_run_id, id DESC);
CREATE INDEX ix_strategy_scorecard_evaluations_policy
  ON strategy_scorecard_evaluations(project_id, portfolio_id, strategy_id, policy_hash, passed);
