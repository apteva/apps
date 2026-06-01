-- Trading v0.5 — environment-backed agent backtests.

CREATE TABLE backtest_runs (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  portfolio_id             INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
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
);

CREATE INDEX ix_backtest_project_status ON backtest_runs(project_id, status, created_at DESC);
CREATE INDEX ix_backtest_portfolio ON backtest_runs(portfolio_id, created_at DESC);

CREATE TABLE backtest_events (
  id          INTEGER PRIMARY KEY,
  run_id      INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
  kind        TEXT    NOT NULL,
  message     TEXT    NOT NULL,
  data        TEXT    NOT NULL DEFAULT '{}',
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_backtest_events_run ON backtest_events(run_id, id DESC);
