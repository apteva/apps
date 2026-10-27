ALTER TABLE eval_experiments ADD COLUMN cancel_requested_at TEXT;
ALTER TABLE eval_runs ADD COLUMN cancel_requested_at TEXT;
CREATE TABLE IF NOT EXISTS eval_run_attempts (
    run_id TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    environment_run_id TEXT NOT NULL DEFAULT '',
    runtime_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    primary_error TEXT NOT NULL DEFAULT '',
    voice_call_json TEXT,
    cleanup_json TEXT,
    started_at TEXT,
    finished_at TEXT,
    PRIMARY KEY(run_id,attempt)
);
