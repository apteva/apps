ALTER TABLE downloads ADD COLUMN stage TEXT NOT NULL DEFAULT 'queued';

UPDATE downloads
SET stage = CASE status
    WHEN 'completed' THEN 'completed'
    WHEN 'failed' THEN 'failed'
    WHEN 'canceled' THEN 'canceled'
    WHEN 'running' THEN 'downloading'
    ELSE 'queued'
END;
