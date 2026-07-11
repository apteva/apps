-- Keep retention deletes and version-qualified lookups indexed as the
-- invocation history grows.
CREATE INDEX IF NOT EXISTS ix_inv_started ON function_invocations(started_at);
CREATE INDEX IF NOT EXISTS ix_fnver_proj_id ON function_versions(project_id, id);
