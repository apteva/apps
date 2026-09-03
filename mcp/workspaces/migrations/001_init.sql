CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  purpose TEXT NOT NULL DEFAULT '',
  profile TEXT NOT NULL,
  image TEXT NOT NULL,
  workload_id TEXT NOT NULL DEFAULT '',
  lifecycle_status TEXT NOT NULL DEFAULT 'provisioning',
  activity_status TEXT NOT NULL DEFAULT 'idle',
  runtime_status TEXT NOT NULL DEFAULT '',
  health_status TEXT NOT NULL DEFAULT '',
  host_label TEXT NOT NULL DEFAULT 'Local Docker',
  network_policy TEXT NOT NULL DEFAULT 'isolated-egress',
  cpu REAL NOT NULL DEFAULT 0,
  memory_mb INTEGER NOT NULL DEFAULT 0,
  consumer_app TEXT NOT NULL DEFAULT 'workspaces',
  consumer_install_id INTEGER NOT NULL DEFAULT 0,
  owner_agent_id INTEGER NOT NULL DEFAULT 0,
  owner_thread_id TEXT NOT NULL DEFAULT '',
  owner_label TEXT NOT NULL DEFAULT '',
  resource_kind TEXT NOT NULL DEFAULT '',
  resource_id TEXT NOT NULL DEFAULT '',
  repo_label TEXT NOT NULL DEFAULT '',
  branch_label TEXT NOT NULL DEFAULT '',
  origin_label TEXT NOT NULL DEFAULT '',
  origin_href TEXT NOT NULL DEFAULT '',
  dirty_state TEXT NOT NULL DEFAULT 'unknown',
  unpushed_state TEXT NOT NULL DEFAULT 'unknown',
  last_error TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_activity_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  delete_at TEXT NOT NULL,
  destroyed_at TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workspaces_active_name
  ON workspaces(project_id, name)
  WHERE lifecycle_status != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS idx_workspaces_idempotency
  ON workspaces(project_id, idempotency_key)
  WHERE idempotency_key != '';

CREATE INDEX IF NOT EXISTS idx_workspaces_project_status
  ON workspaces(project_id, lifecycle_status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspaces_expiry
  ON workspaces(lifecycle_status, expires_at, delete_at);

CREATE TABLE IF NOT EXISTS workspace_commands (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  execution_id TEXT NOT NULL DEFAULT '',
  display_command TEXT NOT NULL,
  argv_json TEXT NOT NULL,
  working_directory TEXT NOT NULL DEFAULT '/workspace',
  timeout_s INTEGER NOT NULL,
  actor_kind TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL DEFAULT '',
  actor_label TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  exit_code INTEGER,
  error_code TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  output_bytes INTEGER NOT NULL DEFAULT 0,
  output_truncated INTEGER NOT NULL DEFAULT 0,
  idempotency_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  FOREIGN KEY(workspace_id) REFERENCES workspaces(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_commands_idempotency
  ON workspace_commands(project_id, idempotency_key)
  WHERE idempotency_key != '';

CREATE INDEX IF NOT EXISTS idx_workspace_commands_workspace
  ON workspace_commands(workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_commands_active
  ON workspace_commands(status, updated_at);

CREATE TABLE IF NOT EXISTS workspace_activity (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  actor_kind TEXT NOT NULL DEFAULT '',
  actor_id TEXT NOT NULL DEFAULT '',
  actor_label TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL,
  data_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  FOREIGN KEY(workspace_id) REFERENCES workspaces(id)
);

CREATE INDEX IF NOT EXISTS idx_workspace_activity_workspace
  ON workspace_activity(workspace_id, id DESC);
