ALTER TABLE media ADD COLUMN derivation_retry_at TEXT;
CREATE TABLE derivation_cleanup_queue (
 project_id TEXT NOT NULL,storage_file_id TEXT NOT NULL,derivation TEXT NOT NULL,
 PRIMARY KEY(project_id,storage_file_id)
);
