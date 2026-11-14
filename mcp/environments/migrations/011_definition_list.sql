CREATE INDEX IF NOT EXISTS idx_environment_definitions_created_id
ON environment_definitions(created_at DESC, id DESC);
