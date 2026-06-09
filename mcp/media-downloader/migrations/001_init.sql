CREATE TABLE IF NOT EXISTS source_profiles (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    auth_type TEXT NOT NULL,
    encrypted_payload TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    last_validated_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_source_profiles_project_name
    ON source_profiles(project_id, name)
    WHERE status != 'deleted';

CREATE TABLE IF NOT EXISTS downloads (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL,
    status TEXT NOT NULL,
    progress REAL NOT NULL DEFAULT 0,
    title TEXT,
    extractor TEXT,
    mode TEXT NOT NULL DEFAULT 'video',
    quality TEXT NOT NULL DEFAULT 'best',
    format_id TEXT,
    source_profile_id TEXT,
    storage_folder TEXT NOT NULL,
    storage_visibility TEXT NOT NULL DEFAULT 'private',
    storage_file_id INTEGER,
    storage_url TEXT,
    output_name TEXT,
    output_bytes INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_downloads_project_updated
    ON downloads(project_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS download_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    download_id TEXT NOT NULL,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_download_logs_download
    ON download_logs(download_id, id DESC);
