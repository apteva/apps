CREATE TABLE IF NOT EXISTS relay_domain_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  hostname TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  dns_managed INTEGER NOT NULL DEFAULT 0,
  dns_domain TEXT NOT NULL DEFAULT '',
  dns_name TEXT NOT NULL DEFAULT '',
  dns_type TEXT NOT NULL DEFAULT '',
  dns_value TEXT NOT NULL DEFAULT '',
  dns_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
