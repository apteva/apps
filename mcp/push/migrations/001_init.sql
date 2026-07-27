CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  token_ciphertext TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  platform TEXT NOT NULL CHECK (platform IN ('ios')),
  user_ref TEXT NOT NULL DEFAULT '',
  app_version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'invalid', 'revoked')),
  last_seen_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS grants (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  instance_ref TEXT NOT NULL,
  secret_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_grants_device ON grants(device_id);
CREATE INDEX IF NOT EXISTS idx_grants_instance ON grants(instance_ref);

CREATE TABLE IF NOT EXISTS deliveries (
  id TEXT PRIMARY KEY,
  grant_id TEXT NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  idempotency_key TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('approval', 'alert', 'report', 'test')),
  item_id TEXT NOT NULL DEFAULT '',
  badge INTEGER,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
  provider_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  sent_at TEXT,
  UNIQUE(grant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_deliveries_device_created
  ON deliveries(device_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_deliveries_status_created
  ON deliveries(status, created_at DESC);
