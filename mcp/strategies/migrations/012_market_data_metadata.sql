-- Normalized market-data provenance and instrument master.

ALTER TABLE marks ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE marks ADD COLUMN timestamp_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE marks ADD COLUMN volume_unit TEXT NOT NULL DEFAULT '';
ALTER TABLE marks ADD COLUMN received_at TIMESTAMP;

ALTER TABLE backtest_market_bars ADD COLUMN volume_unit TEXT NOT NULL DEFAULT '';
ALTER TABLE backtest_market_bars ADD COLUMN timestamp_kind TEXT NOT NULL DEFAULT 'exchange';

CREATE TABLE instruments (
  symbol            TEXT PRIMARY KEY,
  provider_symbol   TEXT NOT NULL,
  name              TEXT NOT NULL DEFAULT '',
  asset_class       TEXT NOT NULL,
  exchange          TEXT NOT NULL,
  exchange_timezone TEXT NOT NULL,
  calendar          TEXT NOT NULL,
  base_currency     TEXT NOT NULL DEFAULT '',
  quote_currency    TEXT NOT NULL DEFAULT '',
  volume_unit       TEXT NOT NULL,
  tick_size         REAL NOT NULL DEFAULT 0,
  lot_size          REAL NOT NULL DEFAULT 0,
  active            INTEGER NOT NULL DEFAULT 1,
  expires_at        TIMESTAMP,
  source            TEXT NOT NULL,
  updated_at        TIMESTAMP NOT NULL
);

CREATE INDEX ix_instruments_asset_exchange ON instruments(asset_class, exchange);
