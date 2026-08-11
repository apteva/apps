ALTER TABLE lessons
ADD COLUMN preview_enabled INTEGER NOT NULL DEFAULT 0
CHECK (preview_enabled IN (0, 1));

CREATE INDEX IF NOT EXISTS idx_lessons_public_previews
ON lessons(preview_enabled, published_at, section_id, position);
