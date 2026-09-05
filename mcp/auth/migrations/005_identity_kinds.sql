-- 005_identity_kinds — guest and external-identity accounts (v0.10.0).
--
-- Player-facing apps need "play before signup": an account keyed by a
-- device id or a studio-issued custom id, upgradable to a full email
-- account later, plus provider identities (Steam, Game Center, Play
-- Games) that the calling app has already verified.
--
-- Additive only. users.kind separates guest rows from account rows;
-- identities reuse oauth_identities (unique per org on provider +
-- provider_user_id). Guest rows keep a synthetic, non-routable email
-- under guest.invalid so the NOT NULL + UNIQUE email contract holds
-- without rebuilding the table.
ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'account';
CREATE INDEX ix_users_kind ON users(project_id, organization_id, kind);
CREATE INDEX ix_oauth_org_user ON oauth_identities(project_id, organization_id, user_id);
