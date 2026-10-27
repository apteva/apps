CREATE TABLE IF NOT EXISTS environment_voice_operations (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_environment_voice_operations_run
ON environment_voice_operations(run_id,status);
