-- Multi-site v2.0 — first half: sites table + default-site seed.
--
-- A site is a self-contained content surface within a project: its
-- own posts/pages/terms/menus/settings/theme, optionally bound to a
-- public hostname. Every existing per-project install gets one
-- "main" site auto-seeded here; migration 004 then adds the site_id
-- partition column to every per-project table and backfills.
--
-- Single-site users see no change: the panel auto-hides the site
-- switcher when only one site exists, and every tool/REST endpoint
-- auto-resolves the site when omitted.

CREATE TABLE sites (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT    NOT NULL,
  slug         TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  hostname     TEXT    NOT NULL DEFAULT '',   -- optional public host
  is_default   INTEGER NOT NULL DEFAULT 0,    -- exactly one live per project
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  archived_at  TIMESTAMP
);

CREATE UNIQUE INDEX sites_slug_uniq
  ON sites (project_id, slug)
  WHERE archived_at IS NULL;

CREATE UNIQUE INDEX sites_hostname_uniq
  ON sites (hostname)
  WHERE hostname != '' AND archived_at IS NULL;

CREATE UNIQUE INDEX sites_default_uniq
  ON sites (project_id)
  WHERE is_default = 1 AND archived_at IS NULL;

-- Seed one default 'main' site per distinct project_id discovered in
-- the existing per-project tables. INSERT OR IGNORE deduplicates
-- safely against the partial unique index (sites_slug_uniq).
INSERT OR IGNORE INTO sites (project_id, slug, name, is_default)
SELECT project_id, 'main', 'Main', 1 FROM (
  SELECT project_id FROM posts WHERE project_id != ''
  UNION
  SELECT project_id FROM terms WHERE project_id != ''
  UNION
  SELECT project_id FROM menus WHERE project_id != ''
  UNION
  SELECT project_id FROM media WHERE project_id != ''
  UNION
  SELECT project_id FROM redirects WHERE project_id != ''
  UNION
  SELECT project_id FROM settings WHERE project_id != ''
);
