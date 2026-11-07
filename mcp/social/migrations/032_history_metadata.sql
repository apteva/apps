-- Per-run metadata evidence and retry budgets survive resumes and restarts.
ALTER TABLE social_history_seen ADD COLUMN metadata_missing INTEGER NOT NULL DEFAULT 0;
ALTER TABLE social_history_seen ADD COLUMN metadata_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE social_history_seen ADD COLUMN metadata_error TEXT NOT NULL DEFAULT '';
ALTER TABLE social_history_seen ADD COLUMN metadata_observed_at TEXT;
