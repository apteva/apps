ALTER TABLE functions ADD COLUMN instance_key TEXT NOT NULL DEFAULT '';
UPDATE functions SET instance_key = lower(hex(randomblob(16)));
ALTER TABLE functions ADD COLUMN deployment_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE functions ADD COLUMN access_json TEXT;
ALTER TABLE function_versions ADD COLUMN artifact_key TEXT NOT NULL DEFAULT '';
UPDATE function_versions SET artifact_key = (SELECT instance_key FROM functions WHERE id = function_id);
ALTER TABLE function_versions ADD COLUMN deployment_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE function_versions ADD COLUMN package_lock TEXT;
ALTER TABLE function_invocations ADD COLUMN version_id INTEGER;
ALTER TABLE function_invocations ADD COLUMN config_hash TEXT;
ALTER TABLE function_invocations ADD COLUMN truncated INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_inv_fn_id ON function_invocations(project_id, function_id, id DESC);

ALTER TABLE function_invocations ADD COLUMN build_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE function_invocations ADD COLUMN queue_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE function_invocations ADD COLUMN cold_start_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE function_invocations ADD COLUMN execution_ms INTEGER NOT NULL DEFAULT 0;
