-- Store provider OAuth callback data while a provider-backed account is being
-- connected. Native OAuth rows leave these columns empty.

ALTER TABLE pending_accounts ADD COLUMN provider_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE pending_accounts ADD COLUMN provider_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pending_accounts ADD COLUMN provider_state TEXT NOT NULL DEFAULT '';
ALTER TABLE pending_accounts ADD COLUMN provider_data TEXT NOT NULL DEFAULT '';
