-- Trading v0.5 — explicit execution environments and richer live marks.

ALTER TABLE portfolios ADD COLUMN execution_environment TEXT NOT NULL DEFAULT 'simulation';
ALTER TABLE portfolios ADD COLUMN live_armed INTEGER NOT NULL DEFAULT 0;

UPDATE portfolios
   SET execution_environment = CASE
     WHEN mode = 'paper' THEN 'simulation'
     ELSE 'broker_live'
   END;

ALTER TABLE marks ADD COLUMN bid_price REAL;
ALTER TABLE marks ADD COLUMN ask_price REAL;
ALTER TABLE marks ADD COLUMN bid_size REAL;
ALTER TABLE marks ADD COLUMN ask_size REAL;
ALTER TABLE marks ADD COLUMN last_trade_price REAL;
ALTER TABLE marks ADD COLUMN last_trade_size REAL;
ALTER TABLE marks ADD COLUMN feed TEXT NOT NULL DEFAULT '';
ALTER TABLE marks ADD COLUMN quote_at TIMESTAMP;

CREATE TABLE market_bars (
  symbol          TEXT NOT NULL,
  timeframe       TEXT NOT NULL,
  bar_at          TIMESTAMP NOT NULL,
  open            REAL NOT NULL,
  high            REAL NOT NULL,
  low             REAL NOT NULL,
  close           REAL NOT NULL,
  volume          REAL NOT NULL DEFAULT 0,
  trade_count     INTEGER NOT NULL DEFAULT 0,
  vwap            REAL NOT NULL DEFAULT 0,
  source          TEXT NOT NULL,
  feed            TEXT NOT NULL DEFAULT '',
  received_at     TIMESTAMP NOT NULL,
  complete        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (symbol, timeframe, bar_at, source, feed)
);
CREATE INDEX ix_market_bars_lookup ON market_bars(symbol, timeframe, bar_at DESC);

CREATE TABLE strategy_run_events (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT NOT NULL,
  portfolio_id      INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
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
CREATE INDEX ix_strategy_run_events_portfolio ON strategy_run_events(portfolio_id, started_at DESC);
