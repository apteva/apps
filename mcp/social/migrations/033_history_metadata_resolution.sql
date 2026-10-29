-- Per-item metadata resolution for listed native IDs: readable, unavailable
-- (provider evidence explains the omission) or unresolved. Evidence is JSON.
ALTER TABLE social_history_seen ADD COLUMN metadata_resolution TEXT NOT NULL DEFAULT '';
ALTER TABLE social_history_seen ADD COLUMN metadata_evidence TEXT NOT NULL DEFAULT '';
UPDATE social_history_seen SET metadata_resolution='readable' WHERE metadata_missing=0;
