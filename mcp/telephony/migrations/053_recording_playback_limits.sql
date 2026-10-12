ALTER TABLE recording_settings ADD COLUMN max_file_bytes INTEGER NOT NULL DEFAULT 1073741824;
ALTER TABLE recordings ADD COLUMN import_blocked_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE recordings ADD COLUMN import_limit_bytes INTEGER NOT NULL DEFAULT 0;
