CREATE TABLE IF NOT EXISTS pcb_bringup_measurements (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  design_id INTEGER NOT NULL,
  revision_id INTEGER NOT NULL,
  board_serial TEXT NOT NULL,
  check_id TEXT NOT NULL,
  value REAL,
  unit TEXT NOT NULL DEFAULT '',
  passed INTEGER NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  measured_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pcb_bringup_revision ON pcb_bringup_measurements(project_id, revision_id, board_serial, check_id, id DESC);

CREATE TABLE IF NOT EXISTS pcb_orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  design_id INTEGER NOT NULL,
  revision_id INTEGER NOT NULL,
  fabricator TEXT NOT NULL,
  quantity INTEGER NOT NULL,
  assembly INTEGER NOT NULL DEFAULT 0,
  options_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'prepared',
  package_artifact_id INTEGER,
  external_order_id TEXT NOT NULL DEFAULT '',
  quote_amount REAL,
  quote_currency TEXT NOT NULL DEFAULT '',
  tracking TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  history_json TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pcb_orders_design ON pcb_orders(project_id, design_id, id DESC);
