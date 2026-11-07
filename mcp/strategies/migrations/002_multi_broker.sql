-- Trading v0.3 — multi-broker. Each portfolio names its broker at
-- creation; the trading sidecar dispatches order/account calls to a
-- registered adapter (brokers/*.go). Paper portfolios leave broker_slug
-- NULL. Live portfolios reject if no adapter exists for the slug at
-- runtime.
--
-- Backfill: any pre-existing live portfolio is binance, since v0.2 was
-- binance-only. New installs of v0.3+ pick a broker explicitly at
-- portfolio_create time.

ALTER TABLE portfolios ADD COLUMN broker_slug TEXT;

UPDATE portfolios
   SET broker_slug = 'binance-trading'
 WHERE mode = 'live' AND broker_slug IS NULL;

CREATE INDEX ix_pf_broker ON portfolios(broker_slug);
