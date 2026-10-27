-- Track deterministic strategy cadence in completed market bars rather than
-- elapsed wall time. Existing assignments are initialized lazily by the
-- runtime so an upgrade cannot place a duplicate order immediately.

ALTER TABLE portfolio_strategy_assignments
  ADD COLUMN last_market_bar_at TIMESTAMP;

ALTER TABLE portfolio_strategy_assignments
  ADD COLUMN last_seen_bar_at TIMESTAMP;

ALTER TABLE portfolio_strategy_assignments
  ADD COLUMN last_checked_at TIMESTAMP;
