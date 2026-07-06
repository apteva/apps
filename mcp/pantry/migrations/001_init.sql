-- pantry v0.1: product definitions plus per-lot stock batches.
--
-- Pantry inventory needs batches/lots, not just one quantity per item:
-- two cartons of milk can have different expiry dates and locations.

CREATE TABLE IF NOT EXISTS locations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL DEFAULT 'pantry'
               CHECK(kind IN ('pantry','fridge','freezer','other')),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

CREATE TABLE IF NOT EXISTS items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    category        TEXT    NOT NULL DEFAULT '',
    barcode         TEXT    NOT NULL DEFAULT '',
    brand           TEXT    NOT NULL DEFAULT '',
    default_unit    TEXT    NOT NULL DEFAULT 'each',
    min_quantity    REAL    NOT NULL DEFAULT 0,
    target_quantity REAL    NOT NULL DEFAULT 0,
    notes           TEXT    NOT NULL DEFAULT '',
    archived        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_items_project_barcode
    ON items(project_id, barcode);

CREATE TABLE IF NOT EXISTS lots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    item_id      INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    location_id  INTEGER NOT NULL REFERENCES locations(id) ON DELETE RESTRICT,
    quantity     REAL    NOT NULL CHECK(quantity >= 0),
    unit         TEXT    NOT NULL DEFAULT 'each',
    expires_at   TEXT,                 -- YYYY-MM-DD
    opened_at    TEXT,                 -- YYYY-MM-DD or RFC3339
    purchased_at TEXT,                 -- YYYY-MM-DD or RFC3339
    source       TEXT    NOT NULL DEFAULT 'human'
                 CHECK(source IN ('human','agent','device','import')),
    notes        TEXT    NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_lots_project_expiry
    ON lots(project_id, expires_at);

CREATE INDEX IF NOT EXISTS idx_lots_project_item
    ON lots(project_id, item_id);

CREATE TABLE IF NOT EXISTS stock_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    lot_id      INTEGER,
    action      TEXT    NOT NULL CHECK(action IN ('add','use','discard','adjust','move')),
    quantity    REAL    NOT NULL,
    unit        TEXT    NOT NULL DEFAULT 'each',
    location_id INTEGER,
    notes       TEXT    NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_stock_events_project_item
    ON stock_events(project_id, item_id, created_at);
