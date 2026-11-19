-- Generic intelligence evidence. Rows are append-only observations; corrections
-- are represented by a new row with a later observed_at and supersedes_id.
CREATE TABLE intelligence_evidence (
  id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'document',
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '{}',
  entity_refs TEXT NOT NULL DEFAULT '[]',
  source TEXT NOT NULL,
  source_ref TEXT NOT NULL DEFAULT '',
  event_time TIMESTAMP,
  published_time TIMESTAMP,
  observed_at TIMESTAMP NOT NULL,
  valid_from TIMESTAMP,
  valid_to TIMESTAMP,
  supersedes_id INTEGER,
  content_hash TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_intel_evidence_hash ON intelligence_evidence(project_id, content_hash);
CREATE INDEX ix_intel_evidence_event ON intelligence_evidence(project_id, event_time DESC);
CREATE INDEX ix_intel_evidence_observed ON intelligence_evidence(project_id, observed_at DESC);
CREATE INDEX ix_intel_evidence_source ON intelligence_evidence(project_id, source, kind);
CREATE INDEX ix_intel_evidence_ref ON intelligence_evidence(project_id, source_ref);
