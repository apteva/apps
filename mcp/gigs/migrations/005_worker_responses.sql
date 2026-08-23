-- Explicit per-instruction worker responses and persisted assignment drafts.
ALTER TABLE gig_upload_sessions ADD COLUMN instruction_key TEXT;
ALTER TABLE gig_upload_sessions ADD COLUMN filename TEXT;
ALTER TABLE gig_upload_sessions ADD COLUMN content_type TEXT;
ALTER TABLE gig_upload_sessions ADD COLUMN size_bytes INTEGER;
ALTER TABLE gig_upload_sessions ADD COLUMN was_existing INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gig_upload_sessions ADD COLUMN discarded_at TIMESTAMP;

CREATE INDEX ix_gig_upload_instruction
  ON gig_upload_sessions(assignment_id, instruction_key, status);

CREATE TABLE gig_assignment_drafts (
  assignment_id            INTEGER PRIMARY KEY REFERENCES gig_assignments(id) ON DELETE CASCADE,
  payload_json             TEXT NOT NULL DEFAULT '{}',
  attachment_file_ids_json TEXT NOT NULL DEFAULT '[]',
  revision                 INTEGER NOT NULL DEFAULT 1,
  updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
