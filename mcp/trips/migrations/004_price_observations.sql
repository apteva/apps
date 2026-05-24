-- trips v0.7 — long-lived travel price intelligence.
--
-- `search_cache` remains the short-lived provider response cache used
-- for actionable booking inventory. This table stores normalized price
-- observations for discovery, trends, and "where is cheap to go" views.
-- It is generic across transport and stays; v0.7 records flights first.

CREATE TABLE IF NOT EXISTS travel_price_observations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         TEXT    NOT NULL,
    trip_id            INTEGER,
    kind               TEXT    NOT NULL, -- flight, train, bus, ferry, hotel, car_rental
    provider           TEXT    NOT NULL,
    search_signature   TEXT    NOT NULL,
    origin_code        TEXT,
    destination_code   TEXT,
    depart_date        TEXT,
    return_date        TEXT,
    party_size         INTEGER NOT NULL DEFAULT 1,
    cabin_or_class     TEXT,
    provider_name      TEXT,
    item_name          TEXT,
    stops_or_transfers INTEGER,
    duration           TEXT,
    amount_cents       INTEGER NOT NULL,
    currency           TEXT    NOT NULL,
    observed_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    live_until         TIMESTAMP,
    bookable_ref       TEXT,
    metadata_json      TEXT    NOT NULL DEFAULT '{}',
    FOREIGN KEY (trip_id) REFERENCES trips(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_travel_price_observations_project_kind
    ON travel_price_observations(project_id, kind, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_travel_price_observations_route
    ON travel_price_observations(project_id, kind, origin_code, destination_code, depart_date);
CREATE INDEX IF NOT EXISTS idx_travel_price_observations_trip
    ON travel_price_observations(trip_id, observed_at DESC);
