ALTER TABLE contact_channels ADD COLUMN verification_verdict TEXT
  CHECK (verification_verdict IS NULL OR verification_verdict IN ('deliverable', 'undeliverable', 'risky', 'unknown'));
ALTER TABLE contact_channels ADD COLUMN verification_confidence TEXT;
ALTER TABLE contact_channels ADD COLUMN verification_reason TEXT;
ALTER TABLE contact_channels ADD COLUMN verification_recommendation TEXT;
ALTER TABLE contact_channels ADD COLUMN verification_source TEXT;
ALTER TABLE contact_channels ADD COLUMN verification_suggested_value TEXT;
ALTER TABLE contact_channels ADD COLUMN verification_checked_at TIMESTAMP;
ALTER TABLE contact_channels ADD COLUMN verification_details TEXT;

CREATE INDEX ix_channel_verification_verdict
  ON contact_channels(project_id, verification_verdict)
  WHERE kind = 'email' AND verification_verdict IS NOT NULL;
