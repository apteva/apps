ALTER TABLE computer_sessions ADD COLUMN proxy_bytes INTEGER;
ALTER TABLE computer_sessions ADD COLUMN usage_status TEXT NOT NULL DEFAULT '';
ALTER TABLE computer_sessions ADD COLUMN usage_measured_at TEXT;
