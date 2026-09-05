CREATE TABLE IF NOT EXISTS object_storages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  provider_connection_id INTEGER NOT NULL DEFAULT 0,
  provider_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'provisioning',
  region TEXT NOT NULL DEFAULT '',
  plan TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL DEFAULT '',
  bucket TEXT NOT NULL DEFAULT '',
  access_key_id TEXT NOT NULL DEFAULT '',
  provider_metadata_json TEXT NOT NULL DEFAULT '{}',
  error_message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(provider, provider_connection_id, provider_id)
);

CREATE INDEX IF NOT EXISTS idx_object_storages_provider
  ON object_storages(provider, provider_connection_id, status);
