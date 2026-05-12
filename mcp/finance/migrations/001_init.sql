-- finance v0.1 — unified wealth tracker.
--
-- Core abstraction: every "thing of value" is one of
--   account     (a place that holds value: cash, brokerage, p2p, crypto,
--                real estate, vehicle, pension, loan)
--   instrument  (the thing being held: AAPL, BTC, EUR, your house, your car)
--   holding     (account × instrument → quantity + cost basis)
-- The ledger is `transactions` — every buy/sell/dividend/interest/
-- deposit/withdraw/transfer/fee/valuation flows through it.
--
-- Money is signed integer minor units (cents/sen/pence) keyed to the
-- transaction's currency. Floats kill correctness; we never use them
-- for amounts. Quantities are REAL (fractional shares + 0.0001 BTC).
--
-- Cash balances are derived (NOT stored as holdings):
--   cash_balance(account) = account.opening_balance + Σ(txns.amount in cash-affecting kinds)
-- This is much simpler than modelling per-currency cash sleeves and
-- supports v0.1's "one currency per account" rule. Multi-currency
-- accounts (a brokerage holding both USD and EUR cash) are a v0.2
-- concern.

CREATE TABLE IF NOT EXISTS settings (
    project_id     TEXT PRIMARY KEY,
    base_currency  TEXT NOT NULL DEFAULT 'EUR',
    week_starts_on TEXT NOT NULL DEFAULT 'mon',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS accounts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK(kind IN
                      ('cash','brokerage','p2p','crypto','real_estate',
                       'vehicle','pension','loan','other')),
    -- 'manual' or 'integration:<provider>' (provider strings reserved
    -- for v0.2: 'integration:trading212','integration:plaid', …).
    source          TEXT    NOT NULL DEFAULT 'manual',
    connection_id   TEXT,                              -- integrations engine handle
    external_id     TEXT,                              -- provider's account id
    currency        TEXT    NOT NULL DEFAULT 'EUR',
    opening_balance INTEGER NOT NULL DEFAULT 0,        -- minor units, account.currency
    opening_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    color           TEXT    NOT NULL DEFAULT '#3b82f6',
    archived        INTEGER NOT NULL DEFAULT 0,
    last_sync_at    TIMESTAMP,
    sync_error      TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, source, external_id)
);
CREATE INDEX IF NOT EXISTS idx_accounts_project ON accounts(project_id, archived);

-- Instruments are split into two scopes:
--   project_id NULL   → shared catalog (stocks, ETFs, crypto, fiat)
--   project_id set    → private (your house, your car, a private P2P book)
-- This keeps AAPL a single row across the workspace while letting
-- real-estate / vehicles stay user-owned.
CREATE TABLE IF NOT EXISTS instruments (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT,
    kind            TEXT    NOT NULL CHECK(kind IN
                      ('stock','etf','fund','bond','crypto','cash',
                       'p2p','real_estate','vehicle','other')),
    symbol          TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    isin            TEXT,
    exchange        TEXT,
    quote_currency  TEXT    NOT NULL,
    metadata        TEXT    NOT NULL DEFAULT '{}',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, kind, symbol)
);
CREATE INDEX IF NOT EXISTS idx_instruments_isin ON instruments(isin);

CREATE TABLE IF NOT EXISTS holdings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    instrument_id INTEGER NOT NULL REFERENCES instruments(id) ON DELETE RESTRICT,
    -- Quantity is in instrument units: shares for stocks, coins for
    -- crypto, 1 for a single real-estate property, outstanding
    -- principal in account.currency for aggregate P2P.
    quantity      REAL    NOT NULL DEFAULT 0,
    -- Running cost basis in account.currency (minor units). Avg-cost
    -- accounting: buy bumps it by amount, sell decrements it
    -- proportionally to qty fraction sold. v0.4 swaps this for lot
    -- tracking when realised-gain tax reports become real.
    cost_basis    INTEGER NOT NULL DEFAULT 0,
    opened_at     TEXT,
    closed_at     TEXT,
    UNIQUE(account_id, instrument_id)
);
CREATE INDEX IF NOT EXISTS idx_holdings_account ON holdings(account_id);

CREATE TABLE IF NOT EXISTS categories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    parent_id   INTEGER REFERENCES categories(id) ON DELETE SET NULL,
    name        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('income','expense')),
    color       TEXT    NOT NULL DEFAULT '#94a3b8',
    archived    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, parent_id, name)
);

CREATE TABLE IF NOT EXISTS transactions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    holding_id      INTEGER REFERENCES holdings(id) ON DELETE SET NULL,
    posted_at       TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK(kind IN (
                      'buy','sell',
                      'dividend','interest',
                      'deposit','withdraw',
                      'transfer',
                      'fee','tax',
                      'income','expense',
                      'valuation'
                    )),
    -- Signed cash flow in `currency`. + into the account, − out of it.
    -- For 'valuation' kind, amount is 0 (the row is an audit marker;
    -- the actual revaluation lives in the `prices` table).
    amount          INTEGER NOT NULL,
    currency        TEXT    NOT NULL,
    -- Instrument units moved (signed). +ve for buy/dividend-in-kind,
    -- −ve for sell. 0 for cash-only kinds.
    quantity        REAL    NOT NULL DEFAULT 0,
    -- Per-unit price in minor units of currency, when applicable.
    price           INTEGER,
    -- Cost-basis delta applied to the holding by this txn, signed,
    -- in account.currency minor units. buy ⇒ +amount; sell ⇒ -prop;
    -- everything else 0. Lets `realised P&L = Σ(amount + cost_basis_delta)`
    -- over sells without re-deriving lots.
    cost_basis_delta INTEGER NOT NULL DEFAULT 0,
    payee           TEXT    NOT NULL DEFAULT '',
    memo            TEXT    NOT NULL DEFAULT '',
    category_id     INTEGER REFERENCES categories(id) ON DELETE SET NULL,
    transfer_id     TEXT,                              -- UUID linking the two halves of a transfer
    external_id     TEXT,                              -- provider txn id, for sync idempotency
    pending         INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(account_id, external_id)
);
CREATE INDEX IF NOT EXISTS idx_txns_account_date ON transactions(account_id, posted_at);
CREATE INDEX IF NOT EXISTS idx_txns_holding_date ON transactions(holding_id, posted_at);
CREATE INDEX IF NOT EXISTS idx_txns_transfer     ON transactions(transfer_id);
CREATE INDEX IF NOT EXISTS idx_txns_category     ON transactions(category_id, posted_at);

-- Sparse time series. Liquid instruments get daily rows from price
-- workers (v0.2+); illiquid get whatever the user enters via
-- valuation_set. Net-worth queries look up the latest price ≤ T.
CREATE TABLE IF NOT EXISTS prices (
    instrument_id INTEGER NOT NULL REFERENCES instruments(id) ON DELETE CASCADE,
    as_of         TEXT    NOT NULL,
    price         INTEGER NOT NULL,                    -- minor units, instrument.quote_currency
    source        TEXT    NOT NULL DEFAULT 'manual',
    PRIMARY KEY(instrument_id, as_of)
);

CREATE TABLE IF NOT EXISTS fx_rates (
    base   TEXT NOT NULL,
    quote  TEXT NOT NULL,
    as_of  TEXT NOT NULL,
    rate   REAL NOT NULL,                              -- 1 base = rate quote
    PRIMARY KEY(base, quote, as_of)
);

-- Default seed: an EUR↔EUR identity rate so single-currency users
-- never hit "missing FX" errors. Real rates land here in v0.2.
INSERT OR IGNORE INTO fx_rates (base, quote, as_of, rate)
VALUES ('EUR', 'EUR', '1970-01-01T00:00:00Z', 1.0);
