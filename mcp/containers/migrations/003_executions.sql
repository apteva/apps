-- Generic workload ownership and asynchronous container executions.

ALTER TABLE containers_workloads ADD COLUMN owner_app_install_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE containers_workloads ADD COLUMN owner_app_name TEXT NOT NULL DEFAULT '';
ALTER TABLE containers_workloads ADD COLUMN project_id TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_containers_workloads_active_name;

CREATE UNIQUE INDEX IF NOT EXISTS idx_containers_workloads_active_owner_name
  ON containers_workloads(project_id, owner_app_install_id, name)
  WHERE status != 'destroyed';

CREATE INDEX IF NOT EXISTS idx_containers_workloads_owner
  ON containers_workloads(owner_app_install_id, project_id, status);

CREATE TABLE IF NOT EXISTS containers_executions (
  id TEXT PRIMARY KEY,
  workload_id TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  owner_app_install_id INTEGER NOT NULL,
  owner_app_name TEXT NOT NULL DEFAULT '',
  argv_json TEXT NOT NULL,
  working_directory TEXT NOT NULL DEFAULT '',
  env_json TEXT NOT NULL DEFAULT '{}',
  timeout_s INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  exit_code INTEGER,
  error_code TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  runtime_container_id TEXT NOT NULL DEFAULT '',
  runtime_container_name TEXT NOT NULL DEFAULT '',
  output TEXT NOT NULL DEFAULT '',
  output_bytes INTEGER NOT NULL DEFAULT 0,
  output_truncated INTEGER NOT NULL DEFAULT 0,
  idempotency_key TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at DATETIME,
  finished_at DATETIME,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_containers_executions_workload
  ON containers_executions(workload_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_containers_executions_active
  ON containers_executions(status, updated_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_containers_executions_idempotency
  ON containers_executions(owner_app_install_id, project_id, idempotency_key)
  WHERE idempotency_key != '';
