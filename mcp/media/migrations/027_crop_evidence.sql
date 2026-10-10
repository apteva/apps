-- Full crop evidence is immutable and project-scoped. Compression keeps the
-- audit off the model context without duplicating large JSON on disk.
CREATE TABLE crop_preview_evidence (
  project_id TEXT NOT NULL,
  evidence_id TEXT NOT NULL,
  payload_gzip BLOB NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, evidence_id)
);
