-- Empty bindings preserve the existing browser-context behavior.
ALTER TABLE computer_contexts ADD COLUMN target_json TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_contexts ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN target_json TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN capabilities_json TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
