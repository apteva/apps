-- Generic provider-backed accounts. Existing rows are direct/native
-- platform connections; provider-backed rows point at a broker
-- integration such as Zernio while keeping Social as the local source
-- of truth for profiles, posts, inbox, and analytics.

ALTER TABLE social_accounts ADD COLUMN provider_slug TEXT NOT NULL DEFAULT 'native';
ALTER TABLE social_accounts ADD COLUMN provider_account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN provider_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN provider_data TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN capabilities TEXT NOT NULL DEFAULT '';

ALTER TABLE post_targets ADD COLUMN provider_post_id TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN provider_data TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_provider_dedupe
  ON social_accounts(project_id, provider_slug, provider_account_id)
  WHERE provider_account_id IS NOT NULL AND provider_account_id != '';

CREATE INDEX IF NOT EXISTS idx_accounts_provider
  ON social_accounts(project_id, provider_slug, status);
