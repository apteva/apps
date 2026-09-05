-- No credentials: retain the original integration identity for cleanup/replay.
CREATE TABLE IF NOT EXISTS computer_provider_leases (
 session_id TEXT PRIMARY KEY,
 backend TEXT NOT NULL,
 provider_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL DEFAULT 0,
 project_id TEXT NOT NULL DEFAULT '',
 provider_project_id TEXT NOT NULL DEFAULT '',
 base_url TEXT NOT NULL DEFAULT '',
 pending INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_after TEXT NOT NULL DEFAULT '',
 terminal_status TEXT NOT NULL DEFAULT 'closed'
);
