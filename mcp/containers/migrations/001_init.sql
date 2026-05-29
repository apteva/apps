-- Containers app v0.1.0 — local Docker workload runtime.

CREATE TABLE IF NOT EXISTS containers_hosts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_id INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'local',
  status TEXT NOT NULL DEFAULT 'unknown',
  docker_available INTEGER NOT NULL DEFAULT 0,
  endpoint TEXT NOT NULL DEFAULT '',
  labels_json TEXT NOT NULL DEFAULT '{}',
  capacity_json TEXT NOT NULL DEFAULT '{}',
  last_probe_at DATETIME,
  last_error TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(instance_id)
);

CREATE TABLE IF NOT EXISTS containers_blueprints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  spec_json TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS containers_workloads (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
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

CREATE INDEX IF NOT EXISTS idx_containers_workloads_status
  ON containers_workloads(status, health_status);

CREATE TABLE IF NOT EXISTS containers_volumes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workload_id TEXT NOT NULL,
  name TEXT NOT NULL,
  docker_volume_name TEXT NOT NULL,
  mount_path TEXT NOT NULL,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  backup_policy_json TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(workload_id, name)
);

CREATE TABLE IF NOT EXISTS containers_ports (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workload_id TEXT NOT NULL,
  protocol TEXT NOT NULL DEFAULT 'tcp',
  container_port INTEGER NOT NULL,
  host_port INTEGER NOT NULL,
  bind_addr TEXT NOT NULL DEFAULT '127.0.0.1',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(workload_id, container_port, protocol)
);

CREATE TABLE IF NOT EXISTS containers_routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workload_id TEXT NOT NULL,
  hostname TEXT NOT NULL UNIQUE,
  target_url TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  cert_status TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS containers_backups (
  id TEXT PRIMARY KEY,
  workload_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  archive_ref TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS containers_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workload_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_containers_events_workload
  ON containers_events(workload_id, id DESC);
