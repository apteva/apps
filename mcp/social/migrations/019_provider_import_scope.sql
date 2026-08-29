-- Provider account connections are used for publishing, analytics, and
-- optional synchronization. Connecting an account must not implicitly opt
-- the project into importing the provider's complete historical timeline.
ALTER TABLE social_accounts ADD COLUMN provider_import_mode TEXT NOT NULL DEFAULT 'managed_only';

-- A local deletion of provider-imported history is an explicit suppression.
-- Keep the provider identity after the post row is gone so full-history
-- imports and manual imports cannot silently recreate it.
CREATE TABLE IF NOT EXISTS provider_import_tombstones (
    social_account_id INTEGER NOT NULL,
    provider_slug TEXT NOT NULL,
    provider_post_id TEXT NOT NULL,
    deleted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reason TEXT NOT NULL DEFAULT 'local_delete',
    PRIMARY KEY (social_account_id, provider_slug, provider_post_id)
);

CREATE INDEX IF NOT EXISTS idx_provider_import_tombstones_account
    ON provider_import_tombstones(social_account_id, provider_slug);
