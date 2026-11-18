CREATE TABLE fabrication_quotes (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 design_id INTEGER NOT NULL REFERENCES designs(id),
 revision_id INTEGER NOT NULL REFERENCES design_revisions(id),
 request_json TEXT NOT NULL,
 options_json TEXT NOT NULL DEFAULT '[]',
 errors_json TEXT NOT NULL DEFAULT '[]',
 expires_at TEXT,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_fabrication_quotes_design ON fabrication_quotes(project_id, design_id, id);

CREATE TABLE fabrication_orders (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 design_id INTEGER NOT NULL REFERENCES designs(id),
 revision_id INTEGER NOT NULL REFERENCES design_revisions(id),
 quote_id INTEGER NOT NULL REFERENCES fabrication_quotes(id),
 option_id TEXT NOT NULL,
 provider TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 status TEXT NOT NULL,
 idempotency_key TEXT NOT NULL UNIQUE,
 currency TEXT NOT NULL,
 quoted_total REAL NOT NULL,
 draft_total REAL NOT NULL,
 address_id TEXT NOT NULL,
 option_json TEXT NOT NULL,
 draft_json TEXT NOT NULL DEFAULT '{}',
 policy_json TEXT NOT NULL DEFAULT '{}',
 approval_json TEXT,
 provider_order_id TEXT,
 provider_status TEXT,
 tracking_json TEXT NOT NULL DEFAULT '{}',
 error_text TEXT,
 created_by TEXT NOT NULL DEFAULT '',
 submitted_at TEXT,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_fabrication_orders_design ON fabrication_orders(project_id, design_id, id);
CREATE INDEX idx_fabrication_orders_status ON fabrication_orders(status);

CREATE TABLE fabrication_settings (
 project_id TEXT PRIMARY KEY,
 settings_json TEXT NOT NULL,
 updated_by TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
