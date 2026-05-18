-- Multi-site v2.0 — second half: site_id partition column on every
-- per-project table.
--
-- SQLite can ADD COLUMN with a default; it can't add a NOT NULL
-- column with a non-constant default. We add nullable INTEGER, UPDATE
-- to backfill from the project's default site, then leave NULL
-- physically possible but enforced in code (every INSERT goes through
-- Go which fills it). Recreating each table the SQLite way to make
-- the column NOT NULL physically would 3x this migration's surface;
-- we trade physical for code-level enforcement.
--
-- Composite unique indexes that included project_id but not site_id
-- get dropped and recreated with site_id prepended so slugs can
-- collide across sites within a project.

-- ── posts ────────────────────────────────────────────────────────
ALTER TABLE posts ADD COLUMN site_id INTEGER REFERENCES sites(id);
UPDATE posts
  SET site_id = (
    SELECT id FROM sites
    WHERE sites.project_id = posts.project_id
      AND is_default = 1 AND archived_at IS NULL
    LIMIT 1
  )
  WHERE site_id IS NULL;

DROP INDEX IF EXISTS posts_slug_uniq;
CREATE UNIQUE INDEX posts_slug_uniq
  ON posts (project_id, site_id, locale, kind, slug)
  WHERE deleted_at IS NULL;
DROP INDEX IF EXISTS posts_project_status_idx;
DROP INDEX IF EXISTS posts_project_kind_idx;
CREATE INDEX posts_site_status_idx ON posts (project_id, site_id, status, published_at DESC);
CREATE INDEX posts_site_kind_idx   ON posts (project_id, site_id, kind, published_at DESC);

-- ── terms ────────────────────────────────────────────────────────
ALTER TABLE terms ADD COLUMN site_id INTEGER REFERENCES sites(id);
UPDATE terms
  SET site_id = (
    SELECT id FROM sites
    WHERE sites.project_id = terms.project_id
      AND is_default = 1 AND archived_at IS NULL
    LIMIT 1
  )
  WHERE site_id IS NULL;
DROP INDEX IF EXISTS terms_slug_uniq;
CREATE UNIQUE INDEX terms_slug_uniq ON terms (project_id, site_id, kind, slug);

-- ── menus ────────────────────────────────────────────────────────
ALTER TABLE menus ADD COLUMN site_id INTEGER REFERENCES sites(id);
UPDATE menus
  SET site_id = (
    SELECT id FROM sites
    WHERE sites.project_id = menus.project_id
      AND is_default = 1 AND archived_at IS NULL
    LIMIT 1
  )
  WHERE site_id IS NULL;
DROP INDEX IF EXISTS menus_slug_uniq;
CREATE UNIQUE INDEX menus_slug_uniq ON menus (project_id, site_id, slug);

-- ── media ────────────────────────────────────────────────────────
ALTER TABLE media ADD COLUMN site_id INTEGER REFERENCES sites(id);
UPDATE media
  SET site_id = (
    SELECT id FROM sites
    WHERE sites.project_id = media.project_id
      AND is_default = 1 AND archived_at IS NULL
    LIMIT 1
  )
  WHERE site_id IS NULL;
CREATE INDEX media_site_idx ON media (project_id, site_id, uploaded_at DESC);

-- ── redirects ────────────────────────────────────────────────────
ALTER TABLE redirects ADD COLUMN site_id INTEGER REFERENCES sites(id);
UPDATE redirects
  SET site_id = (
    SELECT id FROM sites
    WHERE sites.project_id = redirects.project_id
      AND is_default = 1 AND archived_at IS NULL
    LIMIT 1
  )
  WHERE site_id IS NULL;
DROP INDEX IF EXISTS redirects_from_uniq;
CREATE UNIQUE INDEX redirects_from_uniq ON redirects (project_id, site_id, from_path);

-- ── settings ─────────────────────────────────────────────────────
-- The primary key was (project_id, key). Adding site_id makes it
-- (project_id, site_id, key). PRIMARY KEY can't be added in-place on
-- SQLite; we recreate-table-style.
CREATE TABLE settings_new (
  project_id  TEXT    NOT NULL,
  site_id     INTEGER NOT NULL,
  key         TEXT    NOT NULL,
  value       TEXT    NOT NULL DEFAULT '',
  updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, site_id, key)
);
INSERT INTO settings_new (project_id, site_id, key, value, updated_at)
  SELECT project_id,
         (SELECT id FROM sites WHERE sites.project_id = settings.project_id AND is_default = 1 AND archived_at IS NULL LIMIT 1),
         key, value, updated_at
  FROM settings;
DROP TABLE settings;
ALTER TABLE settings_new RENAME TO settings;

-- Templates catalog is per-project (not per-site) intentionally — the
-- catalog of available templates is shared across sites in a project;
-- only the apply target is site-scoped (handled at call time).
