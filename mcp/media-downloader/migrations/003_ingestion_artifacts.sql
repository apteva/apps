ALTER TABLE downloads ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN warnings_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE downloads ADD COLUMN ingest INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS download_artifacts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    download_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    storage_file_id INTEGER NOT NULL,
    storage_url TEXT,
    name TEXT NOT NULL,
    content_type TEXT,
    bytes INTEGER NOT NULL DEFAULT 0,
    language TEXT,
    caption_source TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY(download_id) REFERENCES downloads(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_download_artifacts_job
    ON download_artifacts(download_id, id);
