CREATE TABLE IF NOT EXISTS tunnel_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  base_domain TEXT NOT NULL,
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

CREATE TABLE IF NOT EXISTS tunnels (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  hostname TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  request_count INTEGER NOT NULL DEFAULT 0,
  bytes_in INTEGER NOT NULL DEFAULT 0,
  bytes_out INTEGER NOT NULL DEFAULT 0,
  last_connected_at TEXT,
  last_disconnected_at TEXT,
  last_request_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tunnels_project_status
  ON tunnels(project_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tunnels_token_hash
  ON tunnels(token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tunnels_active_name
  ON tunnels(name) WHERE status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS idx_tunnels_active_hostname
  ON tunnels(hostname) WHERE status = 'active';
