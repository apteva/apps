-- Trading v0.7 — deterministic strategy layer.
--
-- Strategies are structured JSON programs. Backtests reuse the existing
-- backtest_runs/events/snapshots tables via run_kind=strategy.

CREATE TABLE strategies (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  name                TEXT    NOT NULL,
  description         TEXT    NOT NULL DEFAULT '',
  status              TEXT    NOT NULL DEFAULT 'draft', -- draft | active | archived
  definition_json     TEXT    NOT NULL,
  version             INTEGER NOT NULL DEFAULT 1,
  created_by_agent_id INTEGER,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_strategies_project_status ON strategies(project_id, status, updated_at DESC);

CREATE TABLE portfolio_strategy_assignments (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  portfolio_id        INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  strategy_id         INTEGER NOT NULL REFERENCES strategies(id),
  control_mode        TEXT    NOT NULL DEFAULT 'strategy', -- strategy | hybrid
  status              TEXT    NOT NULL DEFAULT 'active',
  assigned_agent_id   INTEGER,
  cadence             TEXT    NOT NULL DEFAULT '1d',
  last_evaluated_at   TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_strategy_assignments_portfolio ON portfolio_strategy_assignments(portfolio_id, status);

ALTER TABLE backtest_runs ADD COLUMN strategy_id INTEGER;
ALTER TABLE backtest_runs ADD COLUMN run_kind TEXT NOT NULL DEFAULT 'agent';
ALTER TABLE backtest_runs ADD COLUMN strategy_version INTEGER;

CREATE INDEX ix_backtest_strategy ON backtest_runs(strategy_id, created_at DESC);
