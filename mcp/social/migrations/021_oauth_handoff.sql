-- One-time proof that an OAuth callback belongs to the pending account.
-- Existing in-flight callbacks must be restarted after upgrade.
ALTER TABLE pending_accounts ADD COLUMN callback_token_hash TEXT NOT NULL DEFAULT '';
