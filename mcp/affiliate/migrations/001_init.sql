CREATE TABLE IF NOT EXISTS networks (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    key               TEXT NOT NULL UNIQUE,
    name              TEXT NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    connection_ref    TEXT NOT NULL DEFAULT '',
    last_refreshed_at TEXT NOT NULL DEFAULT '',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS offers (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    network_key         TEXT NOT NULL,
    external_id         TEXT NOT NULL,
    merchant_name       TEXT NOT NULL,
    offer_name          TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT '',
    category            TEXT NOT NULL DEFAULT '',
    vertical            TEXT NOT NULL DEFAULT '',
    countries_json      TEXT NOT NULL DEFAULT '[]',
    commission_summary  TEXT NOT NULL DEFAULT '',
    cookie_window       TEXT NOT NULL DEFAULT '',
    tracking_deeplink   INTEGER NOT NULL DEFAULT 0,
    raw_json            TEXT NOT NULL DEFAULT '{}',
    last_refreshed_at   TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(network_key, external_id)
);

CREATE INDEX IF NOT EXISTS idx_offers_search
    ON offers(network_key, status, merchant_name, offer_name);

CREATE TABLE IF NOT EXISTS links (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    network_key      TEXT NOT NULL,
    offer_id         INTEGER REFERENCES offers(id) ON DELETE SET NULL,
    destination_url  TEXT NOT NULL,
    affiliate_url    TEXT NOT NULL,
    short_url        TEXT NOT NULL DEFAULT '',
    redirect_rule_id INTEGER NOT NULL DEFAULT 0,
    campaign         TEXT NOT NULL DEFAULT '',
    subid            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'active'
                     CHECK(status IN ('active','broken','inactive')),
    raw_json         TEXT NOT NULL DEFAULT '{}',
    last_checked_at  TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_links_lookup
    ON links(network_key, offer_id, status, id);

CREATE TABLE IF NOT EXISTS stats_daily (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    date             TEXT NOT NULL,
    network_key      TEXT NOT NULL,
    offer_id         INTEGER NOT NULL DEFAULT 0,
    link_id          INTEGER NOT NULL DEFAULT 0,
    clicks           INTEGER NOT NULL DEFAULT 0,
    conversions      INTEGER NOT NULL DEFAULT 0,
    revenue_cents    INTEGER NOT NULL DEFAULT 0,
    commission_cents INTEGER NOT NULL DEFAULT 0,
    currency         TEXT NOT NULL DEFAULT 'USD',
    raw_json         TEXT NOT NULL DEFAULT '{}',
    updated_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(date, network_key, offer_id, link_id, currency)
);

CREATE INDEX IF NOT EXISTS idx_stats_daily_range
    ON stats_daily(date, network_key, offer_id, link_id);
