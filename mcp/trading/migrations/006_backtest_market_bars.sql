-- Trading v0.8 — immutable real market bars for backtests.
--
-- Backtests must replay captured provider data. We no longer synthesize
-- prices on the fly because that makes strategy results look real when
-- they are only plumbing tests.

CREATE TABLE backtest_market_bars (
  run_id      INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
  step        INTEGER NOT NULL,
  symbol      TEXT    NOT NULL,
  asset_class TEXT    NOT NULL,
  t           INTEGER NOT NULL,
  o           REAL    NOT NULL,
  h           REAL    NOT NULL,
  l           REAL    NOT NULL,
  c           REAL    NOT NULL,
  v           REAL    NOT NULL DEFAULT 0,
  source      TEXT    NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (run_id, step, symbol)
);

CREATE INDEX ix_backtest_market_bars_run_symbol ON backtest_market_bars(run_id, symbol, step);
