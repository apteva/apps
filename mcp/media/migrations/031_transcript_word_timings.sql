-- The candidate lifecycle stays in transcripts. Completed snapshots remain readable
-- during upgrades and after failed attempts; no retry deletes a completed version.
CREATE TABLE transcript_versions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 revision TEXT NOT NULL,
 document TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(project_id,file_id,revision)
);
CREATE INDEX ix_transcript_versions_latest ON transcript_versions(project_id,file_id,id DESC);
ALTER TABLE transcripts ADD COLUMN require_words INTEGER NOT NULL DEFAULT 0;
