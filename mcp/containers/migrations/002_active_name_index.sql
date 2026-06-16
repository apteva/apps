-- Allow a destroyed workload name to be reused while still preventing
-- duplicate active workloads with the same name.

BEGIN;

ALTER TABLE containers_workloads RENAME TO containers_workloads_old;

CREATE TABLE containers_workloads (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  blueprint_slug TEXT NOT NULL DEFAULT '',
  host_id INTEGER NOT NULL DEFAULT 0,
  instance_id INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT 'container',
  image TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'creating',
  desired_status TEXT NOT NULL DEFAULT 'running',
  container_id TEXT NOT NULL DEFAULT '',
  container_name TEXT NOT NULL DEFAULT '',
  network_name TEXT NOT NULL DEFAULT '',
  public_url TEXT NOT NULL DEFAULT '',
  health_status TEXT NOT NULL DEFAULT 'unknown',
  health_path TEXT NOT NULL DEFAULT '/',
  health_url TEXT NOT NULL DEFAULT '',
  config_json TEXT NOT NULL DEFAULT '{}',
  env_json TEXT NOT NULL DEFAULT '{}',
  resources_json TEXT NOT NULL DEFAULT '{}',
  restart_policy TEXT NOT NULL DEFAULT 'unless-stopped',
  last_health_at DATETIME,
  last_error TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO containers_workloads (
  id, name, blueprint_slug, host_id, instance_id, kind, image, status,
  desired_status, container_id, container_name, network_name, public_url,
  health_status, health_path, health_url, config_json, env_json,
  resources_json, restart_policy, last_health_at, last_error, created_at, updated_at
)
SELECT
  id, name, blueprint_slug, host_id, instance_id, kind, image, status,
  desired_status, container_id, container_name, network_name, public_url,
  health_status, health_path, health_url, config_json, env_json,
  resources_json, restart_policy, last_health_at, last_error, created_at, updated_at
FROM containers_workloads_old;

DROP TABLE containers_workloads_old;

CREATE INDEX IF NOT EXISTS idx_containers_workloads_status
  ON containers_workloads(status, health_status);

CREATE UNIQUE INDEX IF NOT EXISTS idx_containers_workloads_active_name
  ON containers_workloads(name)
  WHERE status != 'destroyed';

COMMIT;
