-- Strategies 0.2.0 reads portfolios from Trading instead of copying them,
-- so its own tables stop referencing the copied portfolios table. Rebuild
-- each one without that foreign key; keys between Strategies tables stay.
-- strategy_position_accounting is derived from Trading's fills feed and is
-- recreated empty and refilled from the first fill on the next read.
PRAGMA foreign_keys=OFF;

CREATE TABLE new_backtest_runs (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  portfolio_id             INTEGER NOT NULL,
  source_agent_id          INTEGER NOT NULL,
  environment_id           TEXT,
  environment_agent_id     INTEGER,
  environment_portfolio_id INTEGER,
  name                     TEXT    NOT NULL,
  status                   TEXT    NOT NULL DEFAULT 'queued',
  symbols                  TEXT    NOT NULL DEFAULT '[]',
  start_at                 TEXT    NOT NULL,
  end_at                   TEXT    NOT NULL,
  interval                 TEXT    NOT NULL DEFAULT '1d',
  starting_cash            REAL    NOT NULL,
  fee_bps                  REAL    NOT NULL DEFAULT 0,
  slippage_bps             REAL    NOT NULL DEFAULT 0,
  current_step             INTEGER NOT NULL DEFAULT 0,
  total_steps              INTEGER NOT NULL DEFAULT 0,
  summary_json             TEXT    NOT NULL DEFAULT '{}',
  error                    TEXT,
  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at             TIMESTAMP
, strategy_id INTEGER, run_kind TEXT NOT NULL DEFAULT 'agent', strategy_version INTEGER, adjustment_mode TEXT NOT NULL DEFAULT 'provider_adjusted', reference_manifest_json TEXT NOT NULL DEFAULT '{}');
INSERT INTO new_backtest_runs(id,project_id,portfolio_id,source_agent_id,environment_id,environment_agent_id,environment_portfolio_id,name,status,symbols,start_at,end_at,interval,starting_cash,fee_bps,slippage_bps,current_step,total_steps,summary_json,error,created_at,updated_at,completed_at,strategy_id,run_kind,strategy_version,adjustment_mode,reference_manifest_json) SELECT id,project_id,portfolio_id,source_agent_id,environment_id,environment_agent_id,environment_portfolio_id,name,status,symbols,start_at,end_at,interval,starting_cash,fee_bps,slippage_bps,current_step,total_steps,summary_json,error,created_at,updated_at,completed_at,strategy_id,run_kind,strategy_version,adjustment_mode,reference_manifest_json FROM backtest_runs;
DROP TABLE backtest_runs;
ALTER TABLE new_backtest_runs RENAME TO backtest_runs;
CREATE INDEX ix_backtest_project_status ON backtest_runs(project_id, status, created_at DESC);
CREATE INDEX ix_backtest_portfolio ON backtest_runs(portfolio_id, created_at DESC);
CREATE INDEX ix_backtest_strategy ON backtest_runs(strategy_id, created_at DESC);

CREATE TABLE new_portfolio_strategy_assignments (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  portfolio_id        INTEGER NOT NULL,
  strategy_id         INTEGER NOT NULL REFERENCES strategies(id),
  control_mode        TEXT    NOT NULL DEFAULT 'strategy', -- strategy | hybrid
  status              TEXT    NOT NULL DEFAULT 'active',
  assigned_agent_id   INTEGER,
  cadence             TEXT    NOT NULL DEFAULT '1d',
  last_evaluated_at   TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
, strategy_version INTEGER NOT NULL DEFAULT 1, last_market_bar_at TIMESTAMP, last_seen_bar_at TIMESTAMP, last_checked_at TIMESTAMP);
INSERT INTO new_portfolio_strategy_assignments(id,project_id,portfolio_id,strategy_id,control_mode,status,assigned_agent_id,cadence,last_evaluated_at,created_at,updated_at,strategy_version,last_market_bar_at,last_seen_bar_at,last_checked_at) SELECT id,project_id,portfolio_id,strategy_id,control_mode,status,assigned_agent_id,cadence,last_evaluated_at,created_at,updated_at,strategy_version,last_market_bar_at,last_seen_bar_at,last_checked_at FROM portfolio_strategy_assignments;
DROP TABLE portfolio_strategy_assignments;
ALTER TABLE new_portfolio_strategy_assignments RENAME TO portfolio_strategy_assignments;
CREATE INDEX ix_strategy_assignments_portfolio ON portfolio_strategy_assignments(portfolio_id, status);
CREATE INDEX ix_strategy_assignments_version
  ON portfolio_strategy_assignments(strategy_id, strategy_version, status);

CREATE TABLE new_strategy_run_events (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  portfolio_id      INTEGER NOT NULL,
  assignment_id     INTEGER NOT NULL,
  strategy_id       INTEGER NOT NULL,
  strategy_version  INTEGER NOT NULL,
  signal_bar_at     TIMESTAMP NOT NULL,
  status            TEXT NOT NULL,
  decisions_json    TEXT NOT NULL DEFAULT '[]',
  targets_json      TEXT NOT NULL DEFAULT '[]',
  order_ids_json    TEXT NOT NULL DEFAULT '[]',
  error             TEXT NOT NULL DEFAULT '',
  started_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at      TIMESTAMP,
  UNIQUE (assignment_id, signal_bar_at)
);
INSERT INTO new_strategy_run_events(id,project_id,portfolio_id,assignment_id,strategy_id,strategy_version,signal_bar_at,status,decisions_json,targets_json,order_ids_json,error,started_at,completed_at) SELECT id,project_id,portfolio_id,assignment_id,strategy_id,strategy_version,signal_bar_at,status,decisions_json,targets_json,order_ids_json,error,started_at,completed_at FROM strategy_run_events;
DROP TABLE strategy_run_events;
ALTER TABLE new_strategy_run_events RENAME TO strategy_run_events;
CREATE INDEX ix_strategy_run_events_portfolio ON strategy_run_events(portfolio_id, started_at DESC);
CREATE INDEX ix_strategy_run_events_strategy ON strategy_run_events(strategy_id, started_at DESC);

CREATE TABLE new_strategy_scorecard_policies (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  portfolio_id          INTEGER NOT NULL,
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
INSERT INTO new_strategy_scorecard_policies(id,project_id,portfolio_id,strategy_id,criteria_json,min_completed_runs,require_out_of_sample,enforcement_enabled,promotion_stage,created_at,updated_at) SELECT id,project_id,portfolio_id,strategy_id,criteria_json,min_completed_runs,require_out_of_sample,enforcement_enabled,promotion_stage,created_at,updated_at FROM strategy_scorecard_policies;
DROP TABLE strategy_scorecard_policies;
ALTER TABLE new_strategy_scorecard_policies RENAME TO strategy_scorecard_policies;
CREATE INDEX ix_strategy_scorecard_policy
  ON strategy_scorecard_policies(project_id, strategy_id, promotion_stage);

CREATE TABLE new_strategy_scorecard_evaluations (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT    NOT NULL,
  portfolio_id      INTEGER NOT NULL,
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
INSERT INTO new_strategy_scorecard_evaluations(id,project_id,portfolio_id,strategy_id,strategy_version,backtest_run_id,evaluation_scope,passed,verdict,metrics_json,checks_json,policy_json,policy_hash,dataset_sha256,evaluated_at) SELECT id,project_id,portfolio_id,strategy_id,strategy_version,backtest_run_id,evaluation_scope,passed,verdict,metrics_json,checks_json,policy_json,policy_hash,dataset_sha256,evaluated_at FROM strategy_scorecard_evaluations;
DROP TABLE strategy_scorecard_evaluations;
ALTER TABLE new_strategy_scorecard_evaluations RENAME TO strategy_scorecard_evaluations;
CREATE INDEX ix_strategy_scorecard_evaluations_latest
  ON strategy_scorecard_evaluations(project_id, portfolio_id, strategy_id, id DESC);
CREATE INDEX ix_strategy_scorecard_evaluations_run
  ON strategy_scorecard_evaluations(backtest_run_id, id DESC);
CREATE INDEX ix_strategy_scorecard_evaluations_policy
  ON strategy_scorecard_evaluations(project_id, portfolio_id, strategy_id, policy_hash, passed);

CREATE TABLE new_strategy_rebalances (
 assignment_id INTEGER PRIMARY KEY, portfolio_id INTEGER NOT NULL,
 strategy_id INTEGER NOT NULL, strategy_version INTEGER NOT NULL, targets_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO new_strategy_rebalances(assignment_id,portfolio_id,strategy_id,strategy_version,targets_json,status,updated_at) SELECT assignment_id,portfolio_id,strategy_id,strategy_version,targets_json,status,updated_at FROM strategy_rebalances;
DROP TABLE strategy_rebalances;
ALTER TABLE new_strategy_rebalances RENAME TO strategy_rebalances;

CREATE TABLE new_validation_suites (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 portfolio_id INTEGER NOT NULL,
 source_run_id INTEGER NOT NULL REFERENCES backtest_runs(id),
 name TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued',
 spec_json TEXT NOT NULL,
 source_json TEXT NOT NULL,
 plan_json TEXT NOT NULL,
 plan_sha256 TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO new_validation_suites(id,project_id,portfolio_id,source_run_id,name,status,spec_json,source_json,plan_json,plan_sha256,error,created_at,updated_at) SELECT id,project_id,portfolio_id,source_run_id,name,status,spec_json,source_json,plan_json,plan_sha256,error,created_at,updated_at FROM validation_suites;
DROP TABLE validation_suites;
ALTER TABLE new_validation_suites RENAME TO validation_suites;
CREATE INDEX validation_suites_project ON validation_suites(project_id, id);

DROP TABLE strategy_position_accounting;
CREATE TABLE strategy_position_accounting (
 project_id         TEXT    NOT NULL DEFAULT '',
 portfolio_id       INTEGER NOT NULL,
 strategy_id        INTEGER NOT NULL DEFAULT 0,
 symbol             TEXT    NOT NULL,
 outcome            TEXT    NOT NULL DEFAULT '',
 open_qty           REAL    NOT NULL DEFAULT 0,
 avg_cost           REAL    NOT NULL DEFAULT 0,
 gross_realized_pnl REAL    NOT NULL DEFAULT 0,
 fees_paid          REAL    NOT NULL DEFAULT 0,
 spread_cost        REAL    NOT NULL DEFAULT 0,
 slippage_cost      REAL    NOT NULL DEFAULT 0,
 notional_traded    REAL    NOT NULL DEFAULT 0,
 fill_count         INTEGER NOT NULL DEFAULT 0,
 first_fill_at      TIMESTAMP,
 last_fill_at       TIMESTAMP,
 updated_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (portfolio_id, strategy_id, symbol, outcome)
);
CREATE INDEX ix_strategy_accounting_strategy ON strategy_position_accounting(strategy_id);
CREATE INDEX ix_strategy_accounting_portfolio ON strategy_position_accounting(portfolio_id, strategy_id);
CREATE INDEX ix_strategy_accounting_project ON strategy_position_accounting(project_id);

-- Per project: the last Trading fill folded into strategy_position_accounting.
CREATE TABLE trading_fill_cursor (
 project_id   TEXT PRIMARY KEY,
 last_fill_id INTEGER NOT NULL DEFAULT 0,
 updated_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The Trading installation this Strategies install trades through. A
-- different installation means different portfolio identities.
CREATE TABLE trading_binding (
 project_id        TEXT PRIMARY KEY,
 source_install_id INTEGER NOT NULL,
 bound_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO trading_binding(project_id,source_install_id) SELECT project_id,source_install_id FROM trading_observation_sources;

PRAGMA foreign_keys=ON;
