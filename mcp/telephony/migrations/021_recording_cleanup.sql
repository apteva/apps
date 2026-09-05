ALTER TABLE recordings ADD COLUMN cleanup_next_at TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS recording_orphan_files (
 project_id TEXT NOT NULL, file_id INTEGER NOT NULL, next_attempt_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(project_id,file_id)
);
CREATE INDEX IF NOT EXISTS idx_recording_cleanup ON recordings(project_id,cleanup_next_at);
