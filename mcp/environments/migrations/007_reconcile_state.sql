ALTER TABLE environment_definitions ADD COLUMN reconcile_status TEXT NOT NULL DEFAULT 'healthy';
ALTER TABLE environment_definitions ADD COLUMN reconcile_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE environment_definitions ADD COLUMN reconcile_error TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_definitions ADD COLUMN reconcile_next_at TEXT;
ALTER TABLE environment_definitions ADD COLUMN degraded_at TEXT;

CREATE INDEX IF NOT EXISTS idx_environment_definitions_reconcile
ON environment_definitions(desired_state, reconcile_status, reconcile_next_at);
