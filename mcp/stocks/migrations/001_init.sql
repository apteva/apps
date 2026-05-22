-- stocks v0.1 — read-through stock explorer backed by Yahoo Finance.
--
-- Yahoo is the source of truth; these tables are a thin cache plus a
-- seeded ticker universe so list/filter/search don't hit Yahoo on every
-- call. Stock data is universal (AAPL's price is the same for every
-- project), so nothing here is project-scoped — a global install shares
-- one cache across projects, a project install gets its own copy.
--
-- Money is REAL here (Yahoo returns float dollars). This is cached
-- external market data, not a ledger, so the integer-minor-units rule
-- that governs the finance app deliberately does not apply.

-- The universe: every ticker the explorer knows about. Seeded below;
-- grows as search/get encounter new valid symbols (auto-added in code).
-- last_* are a lazily-warmed snapshot refreshed whenever the symbol is
-- fetched, so list/filter/sort can run without re-hitting Yahoo.
CREATE TABLE IF NOT EXISTS instrument (
    symbol          TEXT PRIMARY KEY,
    name            TEXT NOT NULL DEFAULT '',
    exchange        TEXT NOT NULL DEFAULT '',
    sector          TEXT NOT NULL DEFAULT '',
    currency        TEXT NOT NULL DEFAULT 'USD',
    last_price      REAL,
    last_change_pct REAL,
    last_yield_pct  REAL,
    refreshed_at    INTEGER,                                   -- unix seconds; NULL = never fetched
    added_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

-- Dividend payment history — append-only historical facts, cached hard.
-- amount is per-share in the instrument's currency.
CREATE TABLE IF NOT EXISTS dividend (
    symbol  TEXT NOT NULL,
    ex_date INTEGER NOT NULL,                                  -- unix seconds
    amount  REAL NOT NULL,
    PRIMARY KEY (symbol, ex_date)
);
CREATE INDEX IF NOT EXISTS idx_dividend_symbol ON dividend(symbol, ex_date);

-- Generic TTL blob cache for the volatile per-call responses (get, chart)
-- and freshness markers (div:<symbol>). key examples:
--   "get:AAPL"  "chart:AAPL:1y:1d"  "div:AAPL"
CREATE TABLE IF NOT EXISTS cache (
    key        TEXT PRIMARY KEY,
    json       TEXT NOT NULL,
    fetched_at INTEGER NOT NULL                                -- unix seconds
);

-- Seed universe: a starter set across sectors, dividend-payers
-- well-represented, so the explorer has content on a fresh install.
INSERT OR IGNORE INTO instrument (symbol, name, exchange, sector, currency) VALUES
 ('AAPL', 'Apple Inc.',                     'NMS', 'Technology',             'USD'),
 ('MSFT', 'Microsoft Corporation',          'NMS', 'Technology',             'USD'),
 ('IBM',  'International Business Machines', 'NYQ', 'Technology',             'USD'),
 ('CSCO', 'Cisco Systems, Inc.',            'NMS', 'Technology',             'USD'),
 ('INTC', 'Intel Corporation',              'NMS', 'Technology',             'USD'),
 ('V',    'Visa Inc.',                      'NYQ', 'Financial Services',     'USD'),
 ('JPM',  'JPMorgan Chase & Co.',           'NYQ', 'Financial Services',     'USD'),
 ('JNJ',  'Johnson & Johnson',              'NYQ', 'Healthcare',             'USD'),
 ('ABBV', 'AbbVie Inc.',                    'NYQ', 'Healthcare',             'USD'),
 ('PFE',  'Pfizer Inc.',                    'NYQ', 'Healthcare',             'USD'),
 ('PG',   'The Procter & Gamble Company',   'NYQ', 'Consumer Defensive',     'USD'),
 ('KO',   'The Coca-Cola Company',          'NYQ', 'Consumer Defensive',     'USD'),
 ('PEP',  'PepsiCo, Inc.',                  'NMS', 'Consumer Defensive',     'USD'),
 ('WMT',  'Walmart Inc.',                   'NYQ', 'Consumer Defensive',     'USD'),
 ('MCD',  'McDonald''s Corporation',        'NYQ', 'Consumer Cyclical',      'USD'),
 ('HD',   'The Home Depot, Inc.',           'NYQ', 'Consumer Cyclical',      'USD'),
 ('DIS',  'The Walt Disney Company',        'NYQ', 'Communication Services', 'USD'),
 ('T',    'AT&T Inc.',                       'NYQ', 'Communication Services', 'USD'),
 ('VZ',   'Verizon Communications Inc.',    'NYQ', 'Communication Services', 'USD'),
 ('XOM',  'Exxon Mobil Corporation',        'NYQ', 'Energy',                 'USD'),
 ('CVX',  'Chevron Corporation',            'NYQ', 'Energy',                 'USD'),
 ('CAT',  'Caterpillar Inc.',               'NYQ', 'Industrials',            'USD'),
 ('MMM',  '3M Company',                      'NYQ', 'Industrials',            'USD'),
 ('O',    'Realty Income Corporation',      'NYQ', 'Real Estate',            'USD'),
 ('SCHD', 'Schwab US Dividend Equity ETF',  'PCX', 'ETF',                    'USD'),
 ('VYM',  'Vanguard High Dividend Yield ETF','PCX','ETF',                    'USD'),
 ('SPY',  'SPDR S&P 500 ETF Trust',         'PCX', 'ETF',                    'USD');
