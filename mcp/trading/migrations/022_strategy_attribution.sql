-- Per-strategy realized P&L.
--
-- position_accounting is keyed (portfolio_id, symbol, outcome) with no
-- strategy dimension, so when two strategies hold the same symbol a closing
-- fill has no attributable cost basis. This table keeps a strategy-own
-- average-cost lot book alongside the portfolio one, using the same
-- weighted-average convention as dbApplyFill so the two agree whenever a
-- symbol has a single owner.
--
-- strategy_id 0 is the unattributed bucket: manual and agent orders, plus
-- imported broker history. Every fill lands in exactly one bucket, so the
-- strategy books plus bucket 0 reconcile to the portfolio book.
--
-- Like position_accounting this is rebuilt from the append-only fills ledger
-- on mount (dbRebuildStrategyAttribution), so existing installs recover their
-- history from orders.strategy_id rather than starting at zero.
CREATE TABLE strategy_position_accounting (
 portfolio_id       INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
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

-- Live rollups scan run events by strategy; the existing index is portfolio-first.
CREATE INDEX ix_strategy_run_events_strategy ON strategy_run_events(strategy_id, started_at DESC);
