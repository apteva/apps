-- Disabled until explicitly enabled. Dirty checkpoints and retry times survive restart.
CREATE TABLE repo_auto_sync (
 repo_id INTEGER PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
 enabled INTEGER NOT NULL DEFAULT 0,
 branch TEXT NOT NULL DEFAULT '',
 remote_branch TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'paused',
 fingerprint TEXT NOT NULL DEFAULT '',
 dirty_since INTEGER NOT NULL DEFAULT 0,
 last_change_at INTEGER NOT NULL DEFAULT 0,
 last_check_at INTEGER NOT NULL DEFAULT 0,
 last_sync_at INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT ''
);
