-- Add explicit conversation threading and message direction.
-- parent_external_id remains the parent comment id for nested comments;
-- thread_external_id groups DMs and comment threads in one conversation.

ALTER TABLE inbox_items ADD COLUMN thread_external_id TEXT;
ALTER TABLE inbox_items ADD COLUMN direction TEXT NOT NULL DEFAULT 'inbound';

CREATE INDEX IF NOT EXISTS idx_inbox_thread
  ON inbox_items(project_id, social_account_id, kind, thread_external_id);
