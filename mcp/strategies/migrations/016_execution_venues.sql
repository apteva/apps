-- Generic venue execution rules and execution-cost attribution.
-- Defaults remain in code so new venues work before an operator stores an
-- override. Rows here are explicit venue or symbol-level overrides.
CREATE TABLE venue_execution_profiles (
  venue_slug              TEXT    NOT NULL,
  asset_class             TEXT    NOT NULL,
  symbol                  TEXT    NOT NULL DEFAULT '*',
  status                  TEXT    NOT NULL DEFAULT 'active',
  calendar                TEXT    NOT NULL DEFAULT '24X7',
  session_policy          TEXT    NOT NULL DEFAULT 'continuous',
  maker_fee_bps           REAL    NOT NULL DEFAULT 0,
  taker_fee_bps           REAL    NOT NULL DEFAULT 0,
  fee_currency            TEXT    NOT NULL DEFAULT 'USD',
  spread_model            TEXT    NOT NULL DEFAULT 'quote',
  fallback_spread_bps     REAL    NOT NULL DEFAULT 0,
  slippage_model          TEXT    NOT NULL DEFAULT 'fixed_bps',
  slippage_bps            REAL    NOT NULL DEFAULT 1,
  min_qty                 REAL    NOT NULL DEFAULT 0,
  min_notional            REAL    NOT NULL DEFAULT 0,
  qty_step                REAL    NOT NULL DEFAULT 0,
  price_tick              REAL    NOT NULL DEFAULT 0,
  funding_rate_bps        REAL    NOT NULL DEFAULT 0,
  funding_interval_hours  INTEGER NOT NULL DEFAULT 0,
  supports_post_only      INTEGER NOT NULL DEFAULT 0,
  supports_reduce_only    INTEGER NOT NULL DEFAULT 0,
  source                  TEXT    NOT NULL DEFAULT 'operator',
  updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (venue_slug, asset_class, symbol),
  CHECK (status IN ('active','degraded','maintenance','outage')),
  CHECK (session_policy IN ('continuous','regular_only','venue_managed')),
  CHECK (spread_model IN ('quote','fixed_bps','none')),
  CHECK (slippage_model IN ('fixed_bps','none'))
);

CREATE TABLE execution_costs (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  portfolio_id    INTEGER NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
  order_id        TEXT,
  fill_id         INTEGER,
  venue_slug      TEXT    NOT NULL,
  symbol          TEXT    NOT NULL,
  kind            TEXT    NOT NULL,
  amount          REAL    NOT NULL,
  currency        TEXT    NOT NULL,
  rate_bps        REAL,
  liquidity_role  TEXT,
  provider_event_id TEXT,
  metadata        TEXT    NOT NULL DEFAULT '{}',
  occurred_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('fee','spread','slippage','funding','rebate'))
);
CREATE INDEX ix_execution_costs_pf ON execution_costs(portfolio_id, occurred_at DESC);
CREATE INDEX ix_execution_costs_order ON execution_costs(order_id);
CREATE UNIQUE INDEX ux_execution_cost_event
  ON execution_costs(portfolio_id, venue_slug, kind, provider_event_id)
  WHERE provider_event_id IS NOT NULL AND provider_event_id != '';

ALTER TABLE fills ADD COLUMN fee_currency TEXT NOT NULL DEFAULT 'USD';
ALTER TABLE fills ADD COLUMN liquidity_role TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE fills ADD COLUMN spread_cost REAL NOT NULL DEFAULT 0;
ALTER TABLE fills ADD COLUMN slippage_cost REAL NOT NULL DEFAULT 0;
ALTER TABLE fills ADD COLUMN venue_slug TEXT NOT NULL DEFAULT 'simulation';
ALTER TABLE fills ADD COLUMN fee_source TEXT NOT NULL DEFAULT 'model';
ALTER TABLE orders ADD COLUMN liquidity_role TEXT NOT NULL DEFAULT 'unknown';

ALTER TABLE instruments ADD COLUMN min_qty REAL NOT NULL DEFAULT 0;
ALTER TABLE instruments ADD COLUMN min_notional REAL NOT NULL DEFAULT 0;
