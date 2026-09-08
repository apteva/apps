ALTER TABLE gig_upload_sessions ADD COLUMN transport TEXT NOT NULL DEFAULT 'mcp';
ALTER TABLE gig_upload_sessions ADD COLUMN part_size INTEGER NOT NULL DEFAULT 1048576;
ALTER TABLE gig_upload_sessions ADD COLUMN client_key TEXT NOT NULL DEFAULT '';
ALTER TABLE gig_upload_sessions ADD COLUMN last_error TEXT;
ALTER TABLE gig_upload_sessions ADD COLUMN last_stage TEXT;
ALTER TABLE gig_upload_sessions ADD COLUMN updated_at TIMESTAMP;
UPDATE gig_upload_sessions SET updated_at=COALESCE(completed_at,created_at);
CREATE INDEX ix_gig_upload_resume ON gig_upload_sessions(assignment_id,instruction_key,client_key,status);
CREATE INDEX ix_gig_upload_recovery ON gig_upload_sessions(status,updated_at);
