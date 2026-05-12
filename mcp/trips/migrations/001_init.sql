-- trips v0.1 — plan trips end-to-end with calendar mirroring.
--
-- A trip is the top-level envelope (name, dates, status). Children are
-- destinations (cities + dates), transport_legs (how you get between
-- them), accommodations (where you sleep), activities (what you do).
-- Each cost-bearing item carries both cost_estimated and cost_actual
-- so planned-vs-actual is a single column subtract.
--
-- Calendar mirroring: every trip owns a dedicated calendar (kind=
-- 'custom') created on trip create via calendar.calendars_create.
-- Items with timing (transport, accommodation, activity-with-start)
-- become events in that calendar; the row stores the returned
-- event_id for upsert/delete. When sync_calendar=0 the mirror is
-- silenced for that trip — useful for scratch plans.
--
-- Money is signed integer minor units (cents). Each item carries its
-- own `currency` so booking a Tokyo hotel in JPY while the trip is
-- EUR-headlined is fine; budget_summary aggregates with a 1:1 fallback
-- when currencies mix (FX proper lands in v0.2).

CREATE TABLE IF NOT EXISTS trips (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    purpose         TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'planning'
                    CHECK(status IN ('planning','booked','in_progress','done','cancelled')),
    start_at        TEXT    NOT NULL,
    end_at          TEXT    NOT NULL,
    home_currency   TEXT    NOT NULL DEFAULT 'EUR',
    total_budget    INTEGER,                                   -- nullable; minor units, home_currency
    participants    TEXT    NOT NULL DEFAULT '[]',             -- JSON array of strings
    notes           TEXT    NOT NULL DEFAULT '',
    color           TEXT    NOT NULL DEFAULT '#3b82f6',
    calendar_id     INTEGER,                                   -- NULL if sync disabled or call failed
    sync_calendar   INTEGER NOT NULL DEFAULT 1,
    archived        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_trips_project ON trips(project_id, archived);
CREATE INDEX IF NOT EXISTS idx_trips_dates   ON trips(start_at, end_at);

CREATE TABLE IF NOT EXISTS destinations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id       INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    place_name    TEXT    NOT NULL,
    country       TEXT    NOT NULL DEFAULT '',                 -- ISO 3166-1 alpha-2
    lat           REAL,
    lng           REAL,
    arrive_at     TEXT    NOT NULL,
    depart_at     TEXT    NOT NULL,
    order_idx     INTEGER NOT NULL DEFAULT 0,
    notes         TEXT    NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_destinations_trip ON destinations(trip_id, order_idx);

CREATE TABLE IF NOT EXISTS transport_legs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id             INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    from_destination_id INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    to_destination_id   INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    kind                TEXT    NOT NULL CHECK(kind IN ('flight','train','car','bus','ferry','other')),
    provider            TEXT    NOT NULL DEFAULT '',
    reference           TEXT    NOT NULL DEFAULT '',
    depart_at           TEXT    NOT NULL,
    arrive_at           TEXT    NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_transport_trip ON transport_legs(trip_id, depart_at);

CREATE TABLE IF NOT EXISTS accommodations (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id             INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    destination_id      INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    name                TEXT    NOT NULL,
    kind                TEXT    NOT NULL DEFAULT 'hotel'
                        CHECK(kind IN ('hotel','airbnb','hostel','rental','friend','other')),
    address             TEXT    NOT NULL DEFAULT '',
    check_in_at         TEXT    NOT NULL,
    check_out_at        TEXT    NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_accommodations_trip ON accommodations(trip_id, check_in_at);

CREATE TABLE IF NOT EXISTS activities (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id           INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    destination_id    INTEGER REFERENCES destinations(id) ON DELETE SET NULL,
    name              TEXT    NOT NULL,
    category          TEXT    NOT NULL DEFAULT 'activity'
                      CHECK(category IN ('food','activity','shopping','transport_local','other')),
    start_at          TEXT,
    end_at            TEXT,
    location          TEXT    NOT NULL DEFAULT '',
    cost_estimated    INTEGER,
    cost_actual       INTEGER,
    currency          TEXT    NOT NULL,
    booked            INTEGER NOT NULL DEFAULT 0,
    notes             TEXT    NOT NULL DEFAULT '',
    calendar_event_id INTEGER,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_activities_trip ON activities(trip_id, start_at);

CREATE TABLE IF NOT EXISTS todos (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id     INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    label       TEXT    NOT NULL,
    due_at      TEXT,
    done        INTEGER NOT NULL DEFAULT 0,
    done_at     TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_todos_trip ON todos(trip_id, done);

-- Optional per-category caps. Categories are fixed so planned-vs-cap
-- works without setup. Pass amount=0 in budget_set to clear a cap.
CREATE TABLE IF NOT EXISTS trip_budgets (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    trip_id   INTEGER NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    category  TEXT    NOT NULL CHECK(category IN
                ('transport','lodging','food','activities','shopping','other')),
    amount    INTEGER NOT NULL,
    notes     TEXT    NOT NULL DEFAULT '',
    UNIQUE(trip_id, category)
);
