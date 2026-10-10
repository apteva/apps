ALTER TABLE transcripts ADD COLUMN diagnostics TEXT NOT NULL DEFAULT '{}';

-- Only encoded proxies checked in full may be reused. Evidence is bound to
-- the source/recipe AND uploaded file, rather than to file existence alone.
CREATE TABLE transcript_audio_checks (
 project_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 storage_file_id INTEGER NOT NULL,
 evidence TEXT NOT NULL,
 PRIMARY KEY(project_id,file_id,kind)
);
CREATE TRIGGER transcript_audio_checks_cleanup AFTER DELETE ON media BEGIN
 DELETE FROM transcript_audio_checks WHERE project_id=OLD.project_id AND file_id=OLD.file_id;
END;
