CREATE TABLE IF NOT EXISTS computer_proxy_profiles (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  provider_slug TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  external_ref TEXT NOT NULL DEFAULT '',
  pool_type TEXT NOT NULL DEFAULT 'residential',
  protocol TEXT NOT NULL DEFAULT 'http',
  default_country TEXT NOT NULL DEFAULT '',
  sticky_scope TEXT NOT NULL DEFAULT 'session',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_computer_proxy_profiles_connection
  ON computer_proxy_profiles(connection_id, enabled, name);

ALTER TABLE computer_sessions ADD COLUMN proxy_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN proxy_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN proxy_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN proxy_profile_name TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN proxy_country TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN proxy_sticky_scope TEXT NOT NULL DEFAULT '';
