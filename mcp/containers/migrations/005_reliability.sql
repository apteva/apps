ALTER TABLE containers_executions ADD COLUMN stateful_command INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_containers_volumes_docker_name ON containers_volumes(docker_volume_name);
CREATE INDEX IF NOT EXISTS idx_containers_executions_retention ON containers_executions(project_id, status, finished_at);

CREATE TABLE IF NOT EXISTS containers_archive_pauses (workload_id TEXT PRIMARY KEY, project_id TEXT NOT NULL DEFAULT '', container_name TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS containers_runtime_cleanup (workload_id TEXT PRIMARY KEY, project_id TEXT NOT NULL DEFAULT '', retry_until TEXT NOT NULL);
