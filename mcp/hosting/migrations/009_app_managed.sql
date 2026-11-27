-- App-managed pools: machines another app drives (phones through Mobile).
-- A pool gets managed_by ('' for Instances hosts, 'mobile' for phones) and
-- may be on platform 'android'. A managed machine has no Instances host
-- (instance_id NULL) and is identified by device_ref, the managing app's
-- device id. device_session is the Mobile session Hosting holds on it.
PRAGMA foreign_keys=OFF;

CREATE TABLE hosting_pools_009 (
  key                 TEXT PRIMARY KEY,
  name                TEXT    NOT NULL,
  platform            TEXT    NOT NULL CHECK (platform IN ('macos', 'linux', 'android')),
  managed_by          TEXT    NOT NULL DEFAULT '' CHECK (managed_by IN ('', 'mobile')),
  script_set          TEXT    NOT NULL DEFAULT 'macos',
  execution_enabled   INTEGER NOT NULL DEFAULT 0,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK ((managed_by = '' AND platform IN ('macos', 'linux')) OR (managed_by = 'mobile' AND platform = 'android'))
);
INSERT INTO hosting_pools_009 (key, name, platform, managed_by, script_set, execution_enabled, metadata_json, created_at, updated_at)
  SELECT key, name, platform, '', script_set, execution_enabled, metadata_json, created_at, updated_at FROM hosting_pools;
DROP TABLE hosting_pools;
ALTER TABLE hosting_pools_009 RENAME TO hosting_pools;

CREATE TABLE hosting_machines_009 (
  id                  INTEGER PRIMARY KEY,
  pool_key            TEXT    NOT NULL REFERENCES hosting_pools(key),
  instance_id         INTEGER UNIQUE,
  device_ref          TEXT    NOT NULL DEFAULT '',
  device_session      TEXT    NOT NULL DEFAULT '',
  name                TEXT    NOT NULL DEFAULT '',
  state               TEXT    NOT NULL DEFAULT 'quarantined'
                      CHECK (state IN ('quarantined', 'available', 'reserved', 'preparing', 'assigned',
                                       'suspended', 'cleaning', 'maintenance')),
  state_reason        TEXT    NOT NULL DEFAULT '',
  online              INTEGER NOT NULL DEFAULT 0,
  last_seen_at        TIMESTAMP,
  public_host         TEXT    NOT NULL DEFAULT '',
  public_port         INTEGER NOT NULL DEFAULT 22,
  serial              TEXT    NOT NULL DEFAULT '',
  expected_os         TEXT    NOT NULL DEFAULT '',
  management_user     TEXT    NOT NULL DEFAULT '',
  script_hashes_json  TEXT    NOT NULL DEFAULT '{}',
  validated           INTEGER NOT NULL DEFAULT 0,
  tenant_id           TEXT,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  connect_mode        TEXT    NOT NULL DEFAULT '',
  endpoint_source     TEXT    NOT NULL DEFAULT 'instances',
  endpoint_issue      TEXT    NOT NULL DEFAULT '',
  CHECK (instance_id IS NOT NULL OR device_ref != '')
);
INSERT INTO hosting_machines_009 (id, pool_key, instance_id, name, state, state_reason, online, last_seen_at, public_host, public_port,
  serial, expected_os, management_user, script_hashes_json, validated, tenant_id, metadata_json, created_at, updated_at,
  connect_mode, endpoint_source, endpoint_issue)
  SELECT id, pool_key, instance_id, name, state, state_reason, online, last_seen_at, public_host, public_port,
  serial, expected_os, management_user, script_hashes_json, validated, tenant_id, metadata_json, created_at, updated_at,
  connect_mode, endpoint_source, endpoint_issue
  FROM hosting_machines;
DROP TABLE hosting_machines;
ALTER TABLE hosting_machines_009 RENAME TO hosting_machines;
CREATE INDEX IF NOT EXISTS ix_hosting_machines_pool_state
  ON hosting_machines(pool_key, state);
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_machines_tenant
  ON hosting_machines(tenant_id) WHERE tenant_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_machines_device
  ON hosting_machines(device_ref) WHERE device_ref != '';

PRAGMA foreign_keys=ON;
