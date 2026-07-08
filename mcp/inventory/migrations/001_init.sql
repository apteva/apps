-- Inventory v0.1.0 — reusable stock ledger.

CREATE TABLE inventory_locations (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  code            TEXT    NOT NULL,
  name            TEXT    NOT NULL,
  type            TEXT    NOT NULL DEFAULT 'warehouse',
  address_json    TEXT    NOT NULL DEFAULT '{}',
  active          INTEGER NOT NULL DEFAULT 1,
  metadata_json   TEXT    NOT NULL DEFAULT '{}',
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_inventory_locations_code
  ON inventory_locations(project_id, code);
CREATE INDEX ix_inventory_locations_active
  ON inventory_locations(project_id, active, name);

CREATE TABLE inventory_items (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  sku                   TEXT    NOT NULL,
  name                  TEXT    NOT NULL,
  catalog_product_id     INTEGER,
  catalog_price_id       INTEGER,
  barcode               TEXT    NOT NULL DEFAULT '',
  unit                  TEXT    NOT NULL DEFAULT 'each',
  track_quantity        INTEGER NOT NULL DEFAULT 1,
  allow_backorder       INTEGER NOT NULL DEFAULT 0,
  archived              INTEGER NOT NULL DEFAULT 0,
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_inventory_items_sku
  ON inventory_items(project_id, sku);
CREATE INDEX ix_inventory_items_catalog
  ON inventory_items(project_id, catalog_product_id, catalog_price_id);
CREATE INDEX ix_inventory_items_search
  ON inventory_items(project_id, archived, name);

CREATE TABLE inventory_levels (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT    NOT NULL,
  item_id        INTEGER NOT NULL REFERENCES inventory_items(id) ON DELETE CASCADE,
  location_id    INTEGER NOT NULL REFERENCES inventory_locations(id) ON DELETE CASCADE,
  on_hand        REAL    NOT NULL DEFAULT 0,
  reserved       REAL    NOT NULL DEFAULT 0,
  incoming       REAL    NOT NULL DEFAULT 0,
  safety_stock   REAL    NOT NULL DEFAULT 0,
  updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, item_id, location_id)
);

CREATE INDEX ix_inventory_levels_item
  ON inventory_levels(project_id, item_id);
CREATE INDEX ix_inventory_levels_location
  ON inventory_levels(project_id, location_id);

CREATE TABLE inventory_reservations (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  item_id         INTEGER NOT NULL REFERENCES inventory_items(id) ON DELETE CASCADE,
  location_id     INTEGER NOT NULL REFERENCES inventory_locations(id) ON DELETE CASCADE,
  quantity        REAL    NOT NULL,
  status          TEXT    NOT NULL DEFAULT 'active',
  reference_app   TEXT    NOT NULL DEFAULT '',
  reference_type  TEXT    NOT NULL DEFAULT '',
  reference_id    TEXT    NOT NULL DEFAULT '',
  metadata_json   TEXT    NOT NULL DEFAULT '{}',
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  expires_at      TIMESTAMP,
  committed_at    TIMESTAMP,
  released_at     TIMESTAMP
);

CREATE INDEX ix_inventory_reservations_item
  ON inventory_reservations(project_id, item_id, status);
CREATE INDEX ix_inventory_reservations_location
  ON inventory_reservations(project_id, location_id, status);
CREATE INDEX ix_inventory_reservations_ref
  ON inventory_reservations(project_id, reference_app, reference_type, reference_id, status);

CREATE TABLE inventory_movements (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  item_id         INTEGER NOT NULL REFERENCES inventory_items(id) ON DELETE CASCADE,
  location_id     INTEGER NOT NULL REFERENCES inventory_locations(id) ON DELETE CASCADE,
  type            TEXT    NOT NULL,
  quantity_delta  REAL    NOT NULL DEFAULT 0,
  on_hand_after   REAL    NOT NULL DEFAULT 0,
  reserved_after  REAL    NOT NULL DEFAULT 0,
  reference_app   TEXT    NOT NULL DEFAULT '',
  reference_type  TEXT    NOT NULL DEFAULT '',
  reference_id    TEXT    NOT NULL DEFAULT '',
  reason          TEXT    NOT NULL DEFAULT '',
  actor           TEXT    NOT NULL DEFAULT 'system',
  metadata_json   TEXT    NOT NULL DEFAULT '{}',
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_inventory_movements_item
  ON inventory_movements(project_id, item_id, id DESC);
CREATE INDEX ix_inventory_movements_location
  ON inventory_movements(project_id, location_id, id DESC);
CREATE INDEX ix_inventory_movements_ref
  ON inventory_movements(project_id, reference_app, reference_type, reference_id, id DESC);
