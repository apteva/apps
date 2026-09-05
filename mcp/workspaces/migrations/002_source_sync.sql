ALTER TABLE workspaces ADD COLUMN source_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN source_manifest_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE workspaces ADD COLUMN source_synced_at TEXT NOT NULL DEFAULT '';
