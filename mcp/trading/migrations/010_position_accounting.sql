-- Durable realized P&L survives a position being fully closed and deleted.
-- On mount the app rebuilds this table from the append-only fills ledger, so
-- existing installs receive correct historical values without rewriting cash,
-- positions, orders, or fills.

CREATE TABLE position_accounting (
  portfolio_id       INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  symbol             TEXT    NOT NULL,
  outcome            TEXT    NOT NULL DEFAULT '',
  gross_realized_pnl REAL    NOT NULL DEFAULT 0,
  fees_paid          REAL    NOT NULL DEFAULT 0,
  updated_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (portfolio_id, symbol, outcome)
);

CREATE INDEX ix_position_accounting_pf ON position_accounting(portfolio_id);
