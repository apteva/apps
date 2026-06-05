-- Trading v0.6 — per-step backtest performance snapshots.

CREATE TABLE backtest_snapshots (
  run_id          INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
  step            INTEGER NOT NULL,
  equity          REAL    NOT NULL,
  cash            REAL    NOT NULL,
  buying_power    REAL    NOT NULL DEFAULT 0,
  open_pnl        REAL    NOT NULL DEFAULT 0,
  open_pnl_pct    REAL    NOT NULL DEFAULT 0,
  realized_pnl    REAL    NOT NULL DEFAULT 0,
  exposure        REAL    NOT NULL DEFAULT 0,
  positions_json  TEXT    NOT NULL DEFAULT '[]',
  orders_json     TEXT    NOT NULL DEFAULT '[]',
  prices_json     TEXT    NOT NULL DEFAULT '[]',
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (run_id, step)
);

CREATE INDEX ix_backtest_snapshots_run_step ON backtest_snapshots(run_id, step);
