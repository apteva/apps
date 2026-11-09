CREATE TABLE IF NOT EXISTS social_oauth_reconnects (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    social_account_id INTEGER NOT NULL,
    connection_id INTEGER NOT NULL,
    result TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
