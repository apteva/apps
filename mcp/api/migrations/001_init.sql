CREATE TABLE IF NOT EXISTS apis (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT NOT NULL,
  slug           TEXT NOT NULL,
  name           TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'active',
  hostname       TEXT NOT NULL DEFAULT '',
  dns_mode       TEXT NOT NULL DEFAULT 'manual',
  dns_status     TEXT NOT NULL DEFAULT '',
  ingress_status TEXT NOT NULL DEFAULT '',
  allow_http     INTEGER NOT NULL DEFAULT 0,
  cors_json      TEXT NOT NULL DEFAULT '{}',
  auth_json      TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, slug)
);

CREATE TABLE IF NOT EXISTS api_routes (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  api_id       INTEGER NOT NULL,
  method       TEXT NOT NULL,
  path_pattern TEXT NOT NULL,
  target_kind  TEXT NOT NULL,
  target_ref   TEXT NOT NULL,
  target_path  TEXT NOT NULL DEFAULT '',
  auth_json    TEXT NOT NULL DEFAULT '{}',
  cors_json    TEXT NOT NULL DEFAULT '{}',
  timeout_ms   INTEGER NOT NULL DEFAULT 30000,
  enabled      INTEGER NOT NULL DEFAULT 1,
  priority     INTEGER NOT NULL DEFAULT 100,
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, api_id, method, path_pattern),
  FOREIGN KEY(api_id) REFERENCES apis(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS api_keys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  api_id       INTEGER NOT NULL,
  name         TEXT NOT NULL,
  key_prefix   TEXT NOT NULL,
  key_hash     TEXT NOT NULL,
  status       TEXT NOT NULL DEFAULT 'active',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  revoked_at   TEXT,
  UNIQUE(project_id, key_prefix),
  FOREIGN KEY(api_id) REFERENCES apis(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS api_request_logs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  api_id       INTEGER,
  route_id     INTEGER,
  hostname     TEXT NOT NULL,
  method       TEXT NOT NULL,
  path         TEXT NOT NULL,
  status_code  INTEGER NOT NULL DEFAULT 0,
  target_kind  TEXT NOT NULL DEFAULT '',
  target_ref   TEXT NOT NULL DEFAULT '',
  auth_kind    TEXT NOT NULL DEFAULT '',
  subject      TEXT NOT NULL DEFAULT '',
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  error        TEXT NOT NULL DEFAULT '',
  request_id   TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_api_routes_api_priority
  ON api_routes(project_id, api_id, enabled, priority);

CREATE UNIQUE INDEX IF NOT EXISTS ux_apis_project_hostname_nonempty
  ON apis(project_id, hostname)
  WHERE hostname <> '';

CREATE INDEX IF NOT EXISTS ix_api_logs_api_created
  ON api_request_logs(project_id, api_id, created_at DESC);
