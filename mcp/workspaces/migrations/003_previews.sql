CREATE TABLE workspace_previews (
 workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
 execution_id TEXT NOT NULL DEFAULT '',
 container_port INTEGER NOT NULL,
 host_port INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'starting',
 error TEXT NOT NULL DEFAULT '',
 started_at TEXT NOT NULL
);
