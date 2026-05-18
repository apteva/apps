-- trips v0.4 — flight + place search via Duffel + Google Places.
--
-- Three pieces:
--   1. `settings` holds per-project search config: which Duffel and
--      Google Places connection to use, default passenger count, home
--      airport (default "from" for flight searches), daily budget cap
--      for Places (Google bills it per request after the $200/mo free
--      tier — soft cap to avoid surprise bills).
--   2. `search_cache` absorbs repeat queries with TTLs that match each
--      provider's freshness (flights 10min, places 24h, place_details
--      7d).
--   3. `place_id` columns let us re-fetch live data (rating, photos,
--      hours) for an item without snapshotting it into our DB.

CREATE TABLE IF NOT EXISTS settings (
    project_id                TEXT PRIMARY KEY,
    home_airport              TEXT    NOT NULL DEFAULT '',
    default_passengers        INTEGER NOT NULL DEFAULT 1,
    duffel_connection_id      INTEGER,
    places_connection_id      INTEGER,
    -- Soft cap. Daily Places spend below this is allowed; above this
    -- the search tool returns quota_exceeded=true and the UI hides
    -- the dropdown. Default ~$5/day (5000 cents). 0 = no cap.
    daily_search_budget_cents INTEGER NOT NULL DEFAULT 500,
    created_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS search_cache (
    key        TEXT    PRIMARY KEY,            -- sha256(provider|tool|normalized_input)
    response   TEXT    NOT NULL,                -- JSON body of the result
    cached_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_search_cache_expires ON search_cache(expires_at);

ALTER TABLE destinations   ADD COLUMN place_id TEXT;
ALTER TABLE accommodations ADD COLUMN place_id TEXT;
ALTER TABLE activities     ADD COLUMN place_id TEXT;
