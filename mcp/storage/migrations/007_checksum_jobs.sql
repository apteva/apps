-- Checksum verification is asynchronous for direct multipart uploads and
-- repairable for rows created by older releases. Keep the physical object
-- key independent from the content checksum: legacy direct uploads live
-- under 00/<storage_key> and must never move when their hash is learned.
ALTER TABLE files ADD COLUMN object_key TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN checksum_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE files ADD COLUMN checksum_error TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;

UPDATE files
   SET object_key = (CASE WHEN length(COALESCE(sha256,'')) >= 2
                          THEN substr(sha256,1,2) ELSE '00' END) || '/' || storage_key
 WHERE object_key = '';

UPDATE files
   SET checksum_status = CASE WHEN length(COALESCE(sha256,'')) = 64
                              THEN 'verified' ELSE 'pending' END
 WHERE checksum_status = 'pending';

CREATE TABLE checksum_jobs (
  file_id      INTEGER PRIMARY KEY,
  project_id   TEXT NOT NULL,
  status       TEXT NOT NULL DEFAULT 'pending',
  attempts     INTEGER NOT NULL DEFAULT 0,
  available_at INTEGER NOT NULL DEFAULT 0,
  locked_at    INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT '',
  requested_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX ix_checksum_jobs_ready ON checksum_jobs(status, available_at, file_id);

INSERT OR IGNORE INTO checksum_jobs(file_id, project_id)
  SELECT id, project_id FROM files
   WHERE deleted_at IS NULL AND (sha256 IS NULL OR sha256 = '' OR checksum_status <> 'verified');
