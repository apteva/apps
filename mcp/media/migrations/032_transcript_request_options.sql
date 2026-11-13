-- Existing pending rows are frozen once when claimed after upgrade.
ALTER TABLE transcripts ADD COLUMN request_options TEXT NOT NULL DEFAULT '{}';
