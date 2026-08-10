ALTER TABLE devices ADD COLUMN bundle_id TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN environment TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_devices_bundle_environment
  ON devices(bundle_id, environment, status);
