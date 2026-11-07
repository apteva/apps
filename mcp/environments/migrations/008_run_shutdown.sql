CREATE TABLE IF NOT EXISTS environment_run_shutdown (
    run_id TEXT PRIMARY KEY,
    requested_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT ''
);
