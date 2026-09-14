ALTER TABLE dev_runs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE dev_runs ADD COLUMN workspace_source_digest TEXT NOT NULL DEFAULT '';
