ALTER TABLE calls ADD COLUMN recording_mode TEXT NOT NULL DEFAULT 'off';
ALTER TABLE calls ADD COLUMN recording_channels TEXT NOT NULL DEFAULT 'dual';
ALTER TABLE calls ADD COLUMN recording_storage_mode TEXT NOT NULL DEFAULT 'copy_to_storage';
ALTER TABLE calls ADD COLUMN recording_retention_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN recording_checked_at TEXT NOT NULL DEFAULT '';

ALTER TABLE inbound_routes ADD COLUMN recording_mode TEXT NOT NULL DEFAULT 'inherit';

CREATE TABLE IF NOT EXISTS recording_settings (
    project_id       TEXT PRIMARY KEY,
    default_mode     TEXT NOT NULL DEFAULT 'off',
    channels         TEXT NOT NULL DEFAULT 'dual',
    storage_mode     TEXT NOT NULL DEFAULT 'copy_to_storage',
    retention_days   INTEGER NOT NULL DEFAULT 0,
    updated_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS recordings (
    id                    TEXT PRIMARY KEY,
    call_id               TEXT NOT NULL,
    project_id            TEXT NOT NULL,
    provider              TEXT NOT NULL,
    carrier_connection_id INTEGER NOT NULL,
    provider_recording_id TEXT NOT NULL,
    provider_status       TEXT NOT NULL DEFAULT 'processing',
    channels              INTEGER NOT NULL DEFAULT 1,
    track                 TEXT NOT NULL DEFAULT 'both',
    format                TEXT NOT NULL DEFAULT 'wav',
    duration_ms           INTEGER NOT NULL DEFAULT 0,
    size_bytes            INTEGER NOT NULL DEFAULT 0,
    storage_file_id       INTEGER NOT NULL DEFAULT 0,
    storage_status        TEXT NOT NULL DEFAULT 'pending',
    import_started_at     TEXT NOT NULL DEFAULT '',
    import_attempts       INTEGER NOT NULL DEFAULT 0,
    next_attempt_at       TEXT NOT NULL DEFAULT '',
    last_error            TEXT NOT NULL DEFAULT '',
    provider_deleted_at   TEXT NOT NULL DEFAULT '',
    retention_expires_at  TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL,
    completed_at          TEXT NOT NULL DEFAULT '',
    stored_at             TEXT NOT NULL DEFAULT '',
    deleted_at            TEXT NOT NULL DEFAULT '',
    UNIQUE(provider, carrier_connection_id, provider_recording_id)
);

CREATE INDEX IF NOT EXISTS idx_recordings_call ON recordings(call_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recordings_project ON recordings(project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recordings_import ON recordings(storage_status, next_attempt_at);
