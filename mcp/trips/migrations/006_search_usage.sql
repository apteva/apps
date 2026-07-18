-- trips v0.8.5 — bounded provider usage and cache/observation maintenance.

CREATE TABLE IF NOT EXISTS search_usage (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    provider        TEXT NOT NULL,
    tool            TEXT NOT NULL,
    estimated_cents INTEGER NOT NULL,
    used_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_search_usage_daily
    ON search_usage(project_id, provider, used_at);

CREATE INDEX IF NOT EXISTS idx_travel_price_observations_signature_time
    ON travel_price_observations(project_id, search_signature, observed_at DESC);
