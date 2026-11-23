-- Functions v1.22.0 — named stages.
--
-- A stage is a free-text name (staging, production, ...) that points one
-- function at one of its immutable versions and carries its own settings.
-- Stage vars are layered over the function's env; access / limits are
-- optional overrides (NULL inherits the function's). Functions without
-- stages keep invoking their active version exactly as before.
--
-- version_id / previous_version_id are plain integers, not foreign keys:
-- version pruning and artifact retention refuse to delete a referenced
-- version, and a function delete removes its stages by cascade.
CREATE TABLE IF NOT EXISTS function_stages (
  id                  INTEGER PRIMARY KEY,
  project_id          TEXT    NOT NULL,
  function_id         INTEGER NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
  name                TEXT    NOT NULL,
  version_id          INTEGER NOT NULL,
  previous_version_id INTEGER,
  vars_json           TEXT,
  access_json         TEXT,
  limits_json         TEXT,
  status              TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  rev                 INTEGER NOT NULL DEFAULT 1,
  created_at          TEXT    NOT NULL,
  updated_at          TEXT    NOT NULL,
  UNIQUE(function_id, name)
);
CREATE INDEX IF NOT EXISTS ix_stages_project_name ON function_stages(project_id, name);
CREATE INDEX IF NOT EXISTS ix_stages_version ON function_stages(version_id);
CREATE INDEX IF NOT EXISTS ix_stages_previous ON function_stages(previous_version_id);

-- Invocation history records which stage served a call. Nullable with no
-- default, so SQLite only rewrites the schema, never existing rows.
ALTER TABLE function_invocations ADD COLUMN stage TEXT;
