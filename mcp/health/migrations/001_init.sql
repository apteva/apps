-- health v0.1: flexible metrics log + workouts list + lightweight goals.
--
-- metrics is intentionally schemaless on the 'kind' axis. A small
-- in-binary catalogue (see kindCatalog in main.go) maps known kinds
-- ('weight','sleep_hours','mood','resting_hr','bp_systolic',
-- 'bp_diastolic','steps','water_ml','energy') to a default unit and
-- chart hint, but unknown kinds work too — they just render with a
-- raw axis. Storing one row per reading keeps the door open for
-- device-sync (source='device') without schema changes.
--
-- workouts is list-shaped (one row per session) because the panel
-- renders it differently from metrics — a list with kind/duration
-- chips, not a chart. distance_km / avg_hr / perceived are nullable
-- so 'walked 30min' (no distance, no HR) works without sentinel
-- values polluting the table.

CREATE TABLE IF NOT EXISTS metrics (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    value       REAL    NOT NULL,
    unit        TEXT    NOT NULL DEFAULT '',
    notes       TEXT    NOT NULL DEFAULT '',
    source      TEXT    NOT NULL DEFAULT 'human'
                CHECK(source IN ('human','agent','device')),
    recorded_at TEXT    NOT NULL,                  -- RFC3339 UTC
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_metrics_kind_time
    ON metrics(project_id, kind, recorded_at);

CREATE TABLE IF NOT EXISTS workouts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    kind          TEXT    NOT NULL,                -- run|ride|lift|yoga|walk|swim|hike|other
    started_at    TEXT    NOT NULL,                -- RFC3339 UTC
    duration_min  INTEGER NOT NULL DEFAULT 0,
    distance_km   REAL,
    avg_hr        INTEGER,
    perceived     INTEGER CHECK(perceived BETWEEN 1 AND 10),
    notes         TEXT    NOT NULL DEFAULT '',
    source        TEXT    NOT NULL DEFAULT 'human'
                  CHECK(source IN ('human','agent','device')),
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workouts_time
    ON workouts(project_id, started_at);

CREATE TABLE IF NOT EXISTS goals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    kind        TEXT    NOT NULL,                  -- e.g. 'sleep_hours' or 'workouts'
    op          TEXT    NOT NULL                   -- 'gte'|'lte'|'eq'
                CHECK(op IN ('gte','lte','eq')),
    target      REAL    NOT NULL,
    cadence     TEXT    NOT NULL DEFAULT 'daily'   -- daily|weekly
                CHECK(cadence IN ('daily','weekly')),
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, kind, cadence)
);

-- Tiny key/value table for panel preferences (currently just
-- 'pins' = JSON array of metric kinds). Avoids a dedicated pins
-- table for one feature.
CREATE TABLE IF NOT EXISTS prefs (
    project_id TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY(project_id, key)
);
