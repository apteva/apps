-- places v0.1: a personal map of spots to try and spots you loved.
--
-- One row per place (restaurant, bar, hotel, museum, beach, ...),
-- free-form JSON tags, named lists, and a visit log. Everything is
-- scoped by project_id so a household can share one map per project.

CREATE TABLE IF NOT EXISTS places (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    kind            TEXT    NOT NULL DEFAULT 'other',
    status          TEXT    NOT NULL DEFAULT 'to_try'
                    CHECK(status IN ('to_try','visited','favorite','closed')),
    lat             REAL,
    lng             REAL,
    address         TEXT    NOT NULL DEFAULT '',
    city            TEXT    NOT NULL DEFAULT '',
    country         TEXT    NOT NULL DEFAULT '',
    google_place_id TEXT    NOT NULL DEFAULT '',
    osm_id          TEXT    NOT NULL DEFAULT '',
    google_maps_url TEXT    NOT NULL DEFAULT '',
    website         TEXT    NOT NULL DEFAULT '',
    phone           TEXT    NOT NULL DEFAULT '',
    price_level     INTEGER,             -- 0 (free) .. 4 (very expensive)
    google_rating   REAL,
    my_rating       INTEGER,             -- 1..10
    source_url      TEXT    NOT NULL DEFAULT '',
    notes           TEXT    NOT NULL DEFAULT '',
    tags_json       TEXT    NOT NULL DEFAULT '[]',
    metadata_json   TEXT    NOT NULL DEFAULT '{}',
    visit_count     INTEGER NOT NULL DEFAULT 0,
    last_visited_at TEXT,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_places_project_updated
    ON places(project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_places_project_status
    ON places(project_id, status, kind);
CREATE INDEX IF NOT EXISTS idx_places_project_geo
    ON places(project_id, lat, lng);
CREATE UNIQUE INDEX IF NOT EXISTS idx_places_project_google
    ON places(project_id, google_place_id) WHERE google_place_id <> '';

CREATE TABLE IF NOT EXISTS lists (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    color       TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_lists_project_name
    ON lists(project_id, name COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS list_places (
    list_id   INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    place_id  INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    added_at  TEXT    NOT NULL,
    PRIMARY KEY (list_id, place_id)
);

CREATE INDEX IF NOT EXISTS idx_list_places_place ON list_places(place_id);

CREATE TABLE IF NOT EXISTS visits (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    place_id     INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    visited_on   TEXT    NOT NULL,       -- YYYY-MM-DD
    with_whom    TEXT    NOT NULL DEFAULT '',
    dishes_json  TEXT    NOT NULL DEFAULT '[]',
    spend        REAL,
    currency     TEXT    NOT NULL DEFAULT '',
    rating       INTEGER,                -- 1..10
    notes        TEXT    NOT NULL DEFAULT '',
    details_json TEXT    NOT NULL DEFAULT '{}', -- e.g. hotel room, nightly price, booking source
    created_at   TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_visits_place ON visits(place_id, visited_on DESC);

CREATE TABLE IF NOT EXISTS settings (
    project_id           TEXT PRIMARY KEY,
    places_connection_id INTEGER,
    home_lat             REAL,
    home_lng             REAL,
    home_label           TEXT NOT NULL DEFAULT '',
    updated_at           TEXT NOT NULL DEFAULT ''
);
