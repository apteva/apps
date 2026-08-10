CREATE TABLE IF NOT EXISTS commerce_marketing_channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  store_id INTEGER NOT NULL REFERENCES commerce_stores(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  ad_account_id INTEGER NOT NULL,
  tracking_source_resource_id INTEGER NOT NULL,
  tracking_source_name TEXT NOT NULL DEFAULT '',
  public_config_json TEXT NOT NULL DEFAULT '{}',
  data_sharing_mode TEXT NOT NULL DEFAULT 'browser',
  site_tracking_status TEXT NOT NULL DEFAULT 'not_installed',
  site_tracking_error TEXT NOT NULL DEFAULT '',
  installed_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, store_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_commerce_marketing_channels_store
  ON commerce_marketing_channels(project_id, store_id, provider, status);
