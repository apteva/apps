-- Hosting v2.0.0 — tenants on containers and machines.
--
-- Hosting no longer sells anything. Callers (SaaS or any other app) pick a
-- runtime profile and drive the tenant lifecycle through Hosting's tools.
-- Legacy commerce tables (customers, plans, plan limits, usage events,
-- add-ons, metering periods) are kept for reading old data; 2.0 never
-- writes to them.

PRAGMA foreign_keys=OFF;

CREATE TABLE IF NOT EXISTS hosting_profiles (
  key                 TEXT PRIMARY KEY,
  name                TEXT    NOT NULL,
  backend             TEXT    NOT NULL DEFAULT 'container'
                      CHECK (backend IN ('container', 'machine_pool', 'machine_on_demand')),
  product_key         TEXT    NOT NULL REFERENCES hosting_products(key),
  pool_key            TEXT,
  image               TEXT    NOT NULL DEFAULT '',
  cpu                 REAL    NOT NULL DEFAULT 1,
  memory_mb           INTEGER NOT NULL DEFAULT 1024,
  storage_mb          INTEGER NOT NULL DEFAULT 1024,
  settings_json       TEXT    NOT NULL DEFAULT '{}',
  status              TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- A plan can point at a product row that no longer exists. Give it an
-- archived placeholder so the plan still becomes a profile and the tenants
-- on it keep a valid profile (built-in products are re-seeded on mount).
INSERT OR IGNORE INTO hosting_products (key, name, status, metadata_json)
SELECT DISTINCT COALESCE(b.product_key, 'apteva'), COALESCE(b.product_key, 'apteva') || ' (missing in 1.x)', 'archived',
       json_object('placeholder', 1)
FROM hosting_plans p
LEFT JOIN hosting_plan_bindings b ON b.plan_key = p.key
WHERE NOT EXISTS (SELECT 1 FROM hosting_products pr WHERE pr.key = COALESCE(b.product_key, 'apteva'));

INSERT OR IGNORE INTO hosting_products (key, name, status, metadata_json)
SELECT 'apteva', 'apteva (missing in 1.x)', 'archived', json_object('placeholder', 1)
WHERE NOT EXISTS (SELECT 1 FROM hosting_products WHERE key = 'apteva');

-- Every 1.x plan becomes a container profile with the same key, so existing
-- tenants keep pointing at a valid profile.
INSERT OR IGNORE INTO hosting_profiles (key, name, backend, product_key, image, cpu, memory_mb, storage_mb, metadata_json)
SELECT p.key, p.name, 'container', COALESCE(b.product_key, 'apteva'), p.image, p.cpu, p.memory_mb, p.storage_mb,
       json_object('migrated_from_plan', p.key)
FROM hosting_plans p
LEFT JOIN hosting_plan_bindings b ON b.plan_key = p.key;

-- A tenant whose plan row is gone (1.x ran without foreign keys enforced)
-- gets an archived placeholder profile instead of failing the migration.
INSERT OR IGNORE INTO hosting_profiles (key, name, backend, product_key, status, metadata_json)
SELECT DISTINCT t.plan_key, t.plan_key || ' (missing in 1.x)', 'container', 'apteva', 'archived', json_object('placeholder', 1)
FROM hosting_tenants t
WHERE NOT EXISTS (SELECT 1 FROM hosting_profiles pr WHERE pr.key = t.plan_key);

CREATE TABLE IF NOT EXISTS hosting_pools (
  key                 TEXT PRIMARY KEY,
  name                TEXT    NOT NULL,
  platform            TEXT    NOT NULL CHECK (platform IN ('macos', 'linux')),
  script_set          TEXT    NOT NULL DEFAULT 'macos',
  execution_enabled   INTEGER NOT NULL DEFAULT 0,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosting_machines (
  id                  INTEGER PRIMARY KEY,
  pool_key            TEXT    NOT NULL REFERENCES hosting_pools(key),
  instance_id         INTEGER NOT NULL UNIQUE,
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
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_hosting_machines_pool_state
  ON hosting_machines(pool_key, state);
-- One active tenant per machine.
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_machines_tenant
  ON hosting_machines(tenant_id) WHERE tenant_id IS NOT NULL;

-- Rebuild hosting_tenants: customer and plan become optional legacy columns,
-- machine tenants have no hostname, and every tenant is keyed by the
-- caller's external_ref.
CREATE TABLE hosting_tenants_v2 (
  id                  TEXT PRIMARY KEY,
  external_ref        TEXT    NOT NULL DEFAULT '',
  profile_key         TEXT    NOT NULL REFERENCES hosting_profiles(key),
  backend             TEXT    NOT NULL DEFAULT 'container',
  customer_id         INTEGER,
  subscription_id     INTEGER,
  workload_id         TEXT    NOT NULL DEFAULT '',
  machine_id          INTEGER REFERENCES hosting_machines(id),
  instance_id         INTEGER,
  reservation_id      TEXT,
  slug                TEXT    NOT NULL,
  default_hostname    TEXT    UNIQUE,
  owner_email         TEXT    NOT NULL,
  ssh_user            TEXT    NOT NULL DEFAULT '',
  access_keys_json    TEXT    NOT NULL DEFAULT '[]',
  status              TEXT    NOT NULL DEFAULT 'provisioning',
  apteva_version      TEXT    NOT NULL DEFAULT '',
  image               TEXT    NOT NULL DEFAULT '',
  api_key_enc         TEXT    NOT NULL DEFAULT '',
  last_health_status  TEXT    NOT NULL DEFAULT 'unknown',
  last_error          TEXT    NOT NULL DEFAULT '',
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  released_at         TIMESTAMP,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO hosting_tenants_v2 (
  id, external_ref, profile_key, backend, customer_id, subscription_id, workload_id,
  slug, default_hostname, owner_email, status, apteva_version, image, api_key_enc,
  last_health_status, last_error, metadata_json, released_at, created_at, updated_at)
SELECT id, '', plan_key, 'container', customer_id, subscription_id, workload_id,
       slug, CASE WHEN status = 'deleted' THEN NULL ELSE NULLIF(default_hostname, '') END, owner_email,
       CASE status WHEN 'deleted' THEN 'released' WHEN 'stopped' THEN 'suspended' ELSE status END,
       apteva_version, image, api_key_enc, last_health_status, last_error, metadata_json,
       CASE status WHEN 'deleted' THEN updated_at END, created_at, updated_at
FROM hosting_tenants;

DROP TABLE hosting_tenants;
ALTER TABLE hosting_tenants_v2 RENAME TO hosting_tenants;

CREATE INDEX IF NOT EXISTS ix_hosting_tenants_status
  ON hosting_tenants(status, created_at);
CREATE INDEX IF NOT EXISTS ix_hosting_tenants_profile
  ON hosting_tenants(profile_key, status);
-- One live tenant per caller reference; a released tenant frees the
-- reference so the same account can be provisioned again later.
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_tenants_external_ref
  ON hosting_tenants(external_ref) WHERE external_ref != '' AND status != 'released';

-- Slugs are unique among live tenants; a released tenant gives its slug back.
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_tenants_slug
  ON hosting_tenants(slug) WHERE status != 'released';

CREATE TABLE IF NOT EXISTS hosting_reservations (
  id                  TEXT PRIMARY KEY,
  profile_key         TEXT    NOT NULL REFERENCES hosting_profiles(key),
  machine_id          INTEGER NOT NULL REFERENCES hosting_machines(id),
  external_ref        TEXT    NOT NULL,
  status              TEXT    NOT NULL DEFAULT 'held'
                      CHECK (status IN ('held', 'confirmed', 'released', 'expired')),
  expires_at          TIMESTAMP NOT NULL,
  tenant_id           TEXT,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- A machine can carry only one live hold, and a caller only one hold per profile.
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_reservations_machine_held
  ON hosting_reservations(machine_id) WHERE status = 'held';
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_reservations_ref_held
  ON hosting_reservations(profile_key, external_ref) WHERE status = 'held';

CREATE TABLE IF NOT EXISTS hosting_operations (
  id                  TEXT PRIMARY KEY,
  kind                TEXT    NOT NULL,
  tenant_id           TEXT,
  machine_id          INTEGER,
  instance_id         INTEGER,
  steps_json          TEXT    NOT NULL DEFAULT '[]',
  step                INTEGER NOT NULL DEFAULT 0,
  status              TEXT    NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')),
  lease_token         TEXT    NOT NULL DEFAULT '',
  lease_until         TIMESTAMP,
  not_before          TIMESTAMP,
  step_started_at     TIMESTAMP,
  attempts            INTEGER NOT NULL DEFAULT 0,
  error               TEXT    NOT NULL DEFAULT '',
  log_json            TEXT    NOT NULL DEFAULT '[]',
  actor               TEXT    NOT NULL DEFAULT '',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at         TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_hosting_operations_status
  ON hosting_operations(status, created_at);
CREATE INDEX IF NOT EXISTS ix_hosting_operations_tenant
  ON hosting_operations(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_hosting_operations_machine
  ON hosting_operations(machine_id, created_at DESC);
-- One run at a time per machine and per tenant. Queued runs wait their turn.
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_operations_machine_active
  ON hosting_operations(machine_id) WHERE machine_id IS NOT NULL AND status IN ('running', 'waiting');
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_operations_tenant_active
  ON hosting_operations(tenant_id) WHERE tenant_id IS NOT NULL AND status IN ('running', 'waiting');

PRAGMA foreign_keys=ON;
