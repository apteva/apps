CREATE TABLE verification_evidence (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 revision_id INTEGER NOT NULL REFERENCES design_revisions(id),
 payload_json TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_verification_revision ON verification_evidence(project_id,revision_id);
