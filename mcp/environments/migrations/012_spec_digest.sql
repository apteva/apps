ALTER TABLE environment_definitions ADD COLUMN spec_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_runs ADD COLUMN pinned_environment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_runs ADD COLUMN spec_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE environment_runs ADD COLUMN spec_digest TEXT NOT NULL DEFAULT '';
