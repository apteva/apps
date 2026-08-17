ALTER TABLE calls ADD COLUMN media_status TEXT NOT NULL DEFAULT 'idle';
ALTER TABLE calls ADD COLUMN media_error_message TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN media_connected_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN media_disconnected_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN media_close_code INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN media_close_reason TEXT NOT NULL DEFAULT '';

UPDATE calls
SET media_status = 'disconnected',
    status = 'in-progress'
WHERE status = 'media-disconnected';

UPDATE calls
SET media_status = 'connected'
WHERE media_active <> 0;

CREATE INDEX IF NOT EXISTS idx_calls_media_status
    ON calls(project_id, media_status, updated_at);
