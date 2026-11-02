CREATE TABLE IF NOT EXISTS app_store_backoff (
    connection_id INTEGER PRIMARY KEY,
    failures INTEGER NOT NULL DEFAULT 0,
    next_retry_at TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS release_sync_state (
    release_id INTEGER PRIMARY KEY,
    failures INTEGER NOT NULL DEFAULT 0,
    next_sync_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT ''
);
