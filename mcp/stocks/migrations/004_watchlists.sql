-- 004 — unified watchlists. A watchlist is the "smart playlist" of stocks:
-- it can be rule-driven (saved screen), manual (pinned symbols), or both.
--
--   members = (stocks matching `rules`) ∪ (include pins) − (exclude pins)
--
-- Watchlists are the app's first project-scoped data (everything else —
-- universe, prices, dividends — is universal). rules is a JSON filter blob
-- ({} = pure-manual). Include-pinned symbols join the warmer's hot tier so
-- the stocks you hand-pick stay fresh.
CREATE TABLE IF NOT EXISTS watchlist (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    rules      TEXT    NOT NULL DEFAULT '{}',           -- JSON: sector,min_yield,max_payout,max_pe,min_growth,sort
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    UNIQUE(project_id, name)
);

CREATE TABLE IF NOT EXISTS watchlist_pin (
    watchlist_id INTEGER NOT NULL REFERENCES watchlist(id) ON DELETE CASCADE,
    symbol       TEXT    NOT NULL,
    mode         TEXT    NOT NULL CHECK(mode IN ('include','exclude')),
    note         TEXT    NOT NULL DEFAULT '',
    added_at     INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (watchlist_id, symbol)
);

-- Lets the warmer cheaply find all include-pinned symbols for its hot tier.
CREATE INDEX IF NOT EXISTS idx_watchlist_pin_include
    ON watchlist_pin(symbol) WHERE mode = 'include';
