ALTER TABLE gig_assignments ADD COLUMN mode TEXT NOT NULL DEFAULT 'direct';
ALTER TABLE gig_assignments ADD COLUMN notify_worker INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gig_assignments ADD COLUMN reviewed_at TIMESTAMP;
ALTER TABLE gig_assignments ADD COLUMN token_expires_at TIMESTAMP;
ALTER TABLE gig_assignments ADD COLUMN token_revoked_at TIMESTAMP;

CREATE INDEX ix_assignment_active
  ON gig_assignments(gig_id, status, mode);
CREATE INDEX ix_assignment_expiry
  ON gig_assignments(status, token_expires_at);

CREATE TABLE gig_upload_sessions (
  upload_id      TEXT PRIMARY KEY,
  assignment_id  INTEGER NOT NULL REFERENCES gig_assignments(id) ON DELETE CASCADE,
  project_id     TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'uploading',
  storage_file_id INTEGER,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at   TIMESTAMP
);
CREATE INDEX ix_gig_upload_assignment
  ON gig_upload_sessions(assignment_id, status);
CREATE INDEX ix_gig_upload_file
  ON gig_upload_sessions(assignment_id, storage_file_id);
