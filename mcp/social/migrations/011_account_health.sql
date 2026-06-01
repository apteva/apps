-- Persist the latest account health probe so the panel and agents can
-- see whether a connected destination still works without publishing.

ALTER TABLE social_accounts ADD COLUMN last_check_at TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN last_check_status TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN last_check_error TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN last_check_details TEXT NOT NULL DEFAULT '';
