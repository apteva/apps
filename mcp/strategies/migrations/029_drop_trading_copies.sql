-- Strategies 0.2.0 stopped reading the Trading tables it used to copy; it
-- reads Trading through portfolio_state, research_reference and fills_feed.
-- Drop those copies, the rest of the pre-split Trading schema that came with
-- them, and the old observation bookkeeping.
-- backtest_market_bars keeps security_id as plain text: securities are
-- Trading's, so the foreign key goes.
PRAGMA foreign_keys=OFF;

CREATE TABLE new_backtest_market_bars (
  run_id         INTEGER NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
  step           INTEGER NOT NULL,
  symbol         TEXT    NOT NULL,
  asset_class    TEXT    NOT NULL,
  t              INTEGER NOT NULL,
  o              REAL    NOT NULL,
  h              REAL    NOT NULL,
  l              REAL    NOT NULL,
  c              REAL    NOT NULL,
  v              REAL    NOT NULL DEFAULT 0,
  source         TEXT    NOT NULL,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  volume_unit    TEXT    NOT NULL DEFAULT '',
  timestamp_kind TEXT    NOT NULL DEFAULT 'exchange',
  security_id    TEXT,
  PRIMARY KEY (run_id, step, symbol)
);
INSERT INTO new_backtest_market_bars
  (run_id, step, symbol, asset_class, t, o, h, l, c, v, source, created_at, volume_unit, timestamp_kind, security_id)
SELECT run_id, step, symbol, asset_class, t, o, h, l, c, v, source, created_at, volume_unit, timestamp_kind, security_id
FROM backtest_market_bars;
DROP TABLE backtest_market_bars;
ALTER TABLE new_backtest_market_bars RENAME TO backtest_market_bars;
CREATE INDEX ix_backtest_market_bars_run_symbol ON backtest_market_bars(run_id, symbol, step);

-- The 19 tables 0.1.x refreshed from portfolio_research_snapshot.
DROP TABLE IF EXISTS day_baselines;
DROP TABLE IF EXISTS watchlist;
DROP TABLE IF EXISTS position_accounting;
DROP TABLE IF EXISTS execution_costs;
DROP TABLE IF EXISTS fills;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS positions;
DROP TABLE IF EXISTS portfolio_universe_policies;
DROP TABLE IF EXISTS portfolio_risk_state;
DROP TABLE IF EXISTS portfolio_risk_policies;
DROP TABLE IF EXISTS venue_execution_profiles;
DROP TABLE IF EXISTS instruments;
DROP TABLE IF EXISTS marks;
DROP TABLE IF EXISTS universe_memberships;
DROP TABLE IF EXISTS exchange_sessions;
DROP TABLE IF EXISTS security_listings;
DROP TABLE IF EXISTS security_identifiers;
DROP TABLE IF EXISTS securities;
DROP TABLE IF EXISTS portfolios;

-- Pre-split Trading tables nothing in Strategies reads or writes.
DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS broker_bindings;
DROP TABLE IF EXISTS corporate_action_entitlements;
DROP TABLE IF EXISTS corporate_action_postings;
DROP TABLE IF EXISTS corporate_actions;
DROP TABLE IF EXISTS journal;
DROP TABLE IF EXISTS order_commissions;
DROP TABLE IF EXISTS order_requests;
DROP TABLE IF EXISTS portfolio_bindings;
DROP TABLE IF EXISTS portfolio_objectives;
DROP TABLE IF EXISTS portfolio_revisions;
DROP TABLE IF EXISTS position_history;
DROP TABLE IF EXISTS replay_steps;
DROP TABLE IF EXISTS objective_observations;
DROP TABLE IF EXISTS objective_results;
DROP TABLE IF EXISTS market_bars;
DROP TABLE IF EXISTS reference_data_checkpoints;
DROP TABLE IF EXISTS reference_data_issues;

-- 0.1.x snapshot bookkeeping, replaced by trading_binding in 0.2.0.
DROP TABLE IF EXISTS trading_observation_failures;
DROP TABLE IF EXISTS trading_observation_sources;

PRAGMA foreign_keys=ON;
