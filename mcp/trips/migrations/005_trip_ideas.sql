-- trips v0.8 — first-class trip ideas.
--
-- Planning often starts before dates are known. Relax date columns so
-- trips, destinations, transport legs, and stays can exist as ideas.
-- Calendar mirroring is handled in app code only when both dates are set.

PRAGMA foreign_keys=off;

CREATE TABLE trips_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    purpose         TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'planning'
                    CHECK(status IN ('idea','planning','booked','in_progress','done','cancelled')),
    start_at        TEXT,
    end_at          TEXT,
    home_currency   TEXT    NOT NULL DEFAULT 'EUR',
    total_budget    INTEGER,
    participants    TEXT    NOT NULL DEFAULT '[]',
    notes           TEXT    NOT NULL DEFAULT '',
    color           TEXT    NOT NULL DEFAULT '#3b82f6',
    calendar_id     INTEGER,
    sync_calendar   INTEGER NOT NULL DEFAULT 1,
    archived        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    calendar_event_id INTEGER
);
INSERT INTO trips_new
SELECT id, project_id, name, purpose, status, start_at, end_at, home_currency,
       total_budget, participants, notes, color, calendar_id, sync_calendar,
       archived, created_at, updated_at, calendar_event_id
FROM trips;
DROP TABLE trips;
ALTER TABLE trips_new RENAME TO trips;
CREATE INDEX IF NOT EXISTS idx_trips_project ON trips(project_id, archived);
CREATE INDEX IF NOT EXISTS idx_trips_dates   ON trips(start_at, end_at);

CREATE TABLE destinations_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id       INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    place_name    TEXT    NOT NULL,
    country       TEXT    NOT NULL DEFAULT '',
    lat           REAL,
    lng           REAL,
    arrive_at     TEXT,
    depart_at     TEXT,
    order_idx     INTEGER NOT NULL DEFAULT 0,
    notes         TEXT    NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    calendar_event_id INTEGER
);
INSERT INTO destinations_new
SELECT id, trip_id, place_name, country, lat, lng, arrive_at, depart_at,
       order_idx, notes, created_at, calendar_event_id
FROM destinations;
DROP TABLE destinations;
ALTER TABLE destinations_new RENAME TO destinations;
CREATE INDEX IF NOT EXISTS idx_destinations_trip ON destinations(trip_id, order_idx);

CREATE TABLE transport_legs_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id             INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    from_destination_id INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    to_destination_id   INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    kind                TEXT    NOT NULL CHECK(kind IN ('flight','train','car','bus','ferry','other')),
    provider            TEXT    NOT NULL DEFAULT '',
    reference           TEXT    NOT NULL DEFAULT '',
    depart_at           TEXT,
    arrive_at           TEXT,
    depart_location     TEXT    NOT NULL DEFAULT '',
    arrive_location     TEXT    NOT NULL DEFAULT '',
    cost_estimated      INTEGER,
    cost_actual         INTEGER,
    currency            TEXT    NOT NULL,
    confirmation_number TEXT    NOT NULL DEFAULT '',
    booked              INTEGER NOT NULL DEFAULT 0,
    notes               TEXT    NOT NULL DEFAULT '',
    calendar_event_id   INTEGER,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO transport_legs_new
SELECT id, trip_id, from_destination_id, to_destination_id, kind, provider,
       reference, depart_at, arrive_at, depart_location, arrive_location,
       cost_estimated, cost_actual, currency, confirmation_number, booked,
       notes, calendar_event_id, created_at, updated_at
FROM transport_legs;
DROP TABLE transport_legs;
ALTER TABLE transport_legs_new RENAME TO transport_legs;
CREATE INDEX IF NOT EXISTS idx_transport_trip ON transport_legs(trip_id, depart_at);

CREATE TABLE accommodations_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id             INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    destination_id      INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    name                TEXT    NOT NULL,
    kind                TEXT    NOT NULL DEFAULT 'hotel'
                        CHECK(kind IN ('hotel','airbnb','hostel','rental','friend','other')),
    address             TEXT    NOT NULL DEFAULT '',
    check_in_at         TEXT,
    check_out_at        TEXT,
    cost_estimated      INTEGER,
    cost_actual         INTEGER,
    currency            TEXT    NOT NULL,
    confirmation_number TEXT    NOT NULL DEFAULT '',
    booked              INTEGER NOT NULL DEFAULT 0,
    notes               TEXT    NOT NULL DEFAULT '',
    calendar_event_id   INTEGER,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO accommodations_new
SELECT id, trip_id, destination_id, name, kind, address, check_in_at,
       check_out_at, cost_estimated, cost_actual, currency,
       confirmation_number, booked, notes, calendar_event_id,
       created_at, updated_at
FROM accommodations;
DROP TABLE accommodations;
ALTER TABLE accommodations_new RENAME TO accommodations;
CREATE INDEX IF NOT EXISTS idx_accommodations_trip ON accommodations(trip_id, check_in_at);

PRAGMA foreign_keys=on;
