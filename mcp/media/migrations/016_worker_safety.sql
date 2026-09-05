-- Manual requests are durable, including when automatic discovery is disabled.
ALTER TABLE media ADD COLUMN describe_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media ADD COLUMN prose_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media ADD COLUMN audience_revision INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER media_prose_revision AFTER UPDATE OF description, description_source, title, alt_text ON media
BEGIN
 UPDATE media SET prose_revision=prose_revision+1 WHERE project_id=NEW.project_id AND file_id=NEW.file_id;
END;
CREATE TRIGGER media_audience_revision AFTER UPDATE OF audience_rating, audience_reasoning ON media
BEGIN
 UPDATE media SET audience_revision=audience_revision+1 WHERE project_id=NEW.project_id AND file_id=NEW.file_id;
END;
-- Outputs with a failed completion commit are retained for reconciliation.
-- Storage may share a deduplicated output with another successful render.
CREATE TABLE render_uncommitted_outputs (
 render_id INTEGER PRIMARY KEY, project_id TEXT NOT NULL, storage_file_id TEXT NOT NULL,
 reason TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
