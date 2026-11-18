ALTER TABLE workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT 'dev';
ALTER TABLE workspaces ADD COLUMN network_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN cache_volume_id TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN cache_key TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN results_exported_at TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_commands ADD COLUMN run_user TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_commands ADD COLUMN network TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_commands ADD COLUMN image TEXT NOT NULL DEFAULT '';
