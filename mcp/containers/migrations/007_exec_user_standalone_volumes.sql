-- 0.7.0: per-command user/umask and standalone named volumes.
ALTER TABLE containers_executions ADD COLUMN exec_user TEXT NOT NULL DEFAULT '';
ALTER TABLE containers_executions ADD COLUMN exec_umask TEXT NOT NULL DEFAULT '';
ALTER TABLE containers_volumes ADD COLUMN volume_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS containers_standalone_volumes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  docker_volume_name TEXT NOT NULL UNIQUE,
  host_id INTEGER NOT NULL DEFAULT 0,
  project_id TEXT NOT NULL DEFAULT '',
  owner_app_install_id INTEGER NOT NULL DEFAULT 0,
  owner_app_name TEXT NOT NULL DEFAULT '',
  labels_json TEXT NOT NULL DEFAULT '{}',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_containers_standalone_volumes_owner_name
  ON containers_standalone_volumes(project_id, owner_app_install_id, name);
CREATE INDEX IF NOT EXISTS idx_containers_volumes_volume_id ON containers_volumes(volume_id);
