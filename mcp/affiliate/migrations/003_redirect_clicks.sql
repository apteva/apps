CREATE TABLE IF NOT EXISTS redirect_clicks_daily (
    date             TEXT NOT NULL,
    redirect_rule_id INTEGER NOT NULL,
    link_id          INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    clicks           INTEGER NOT NULL DEFAULT 0,
    hits_total       INTEGER NOT NULL DEFAULT 0,
    matched_by       TEXT NOT NULL DEFAULT '',
    last_event_at    TEXT NOT NULL DEFAULT '',
    raw_json         TEXT NOT NULL DEFAULT '{}',
    updated_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (date, redirect_rule_id)
);

CREATE INDEX IF NOT EXISTS idx_redirect_clicks_link_date
    ON redirect_clicks_daily(link_id, date);
