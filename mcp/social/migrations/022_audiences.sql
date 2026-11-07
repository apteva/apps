-- Optional audiences reuse the existing integration connection.
CREATE TABLE social_membership_cache (
 social_account_id INTEGER PRIMARY KEY REFERENCES social_accounts(id) ON DELETE CASCADE,
 connection_id INTEGER NOT NULL,
 status TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 levels TEXT NOT NULL DEFAULT '[]',
 checked_at TEXT NOT NULL,
 successful_at TEXT NOT NULL DEFAULT ''
);
ALTER TABLE post_targets ADD COLUMN audience_status TEXT NOT NULL DEFAULT 'not_required';
ALTER TABLE post_targets ADD COLUMN audience_confirmed_at TEXT;
ALTER TABLE post_targets ADD COLUMN audience_confirmation_actor TEXT;
ALTER TABLE post_targets ADD COLUMN youtube_upload_session TEXT;
ALTER TABLE post_targets ADD COLUMN youtube_upload_fingerprint TEXT;
ALTER TABLE post_targets ADD COLUMN youtube_upload_retry_after TEXT;
