-- Profiles: brand/client/site containers within a project. One
-- project, one social install, many profiles. Each profile groups
-- several social_accounts (one FB Page + IG Business + X handle =
-- one profile). Layer 1 of the multi-brand design — accounts get
-- a profile_id and the existing tools accept optional `profile`
-- (slug) / `profile_id` to filter or scope writes.
--
-- profile_id=0 means "unassigned" — every existing row gets that
-- on migration. The panel/MCP show those rows in an "Unassigned"
-- group; users opt into profiles by creating one and moving rows.
CREATE TABLE profiles (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  slug        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  color       TEXT    NOT NULL DEFAULT '',
  is_default  INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, slug)
);

ALTER TABLE social_accounts  ADD COLUMN profile_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE posts            ADD COLUMN profile_id INTEGER NOT NULL DEFAULT 0;
-- pending_accounts carries the operator's chosen profile across the
-- OAuth roundtrip so finalize knows where to land the new
-- social_account row. 0 = the project's default profile (or
-- 'unassigned' if no default is set).
ALTER TABLE pending_accounts ADD COLUMN profile_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_accounts_profile ON social_accounts(profile_id);
CREATE INDEX ix_posts_profile    ON posts(profile_id);
