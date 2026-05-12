-- content v1 — initial schema.
--
-- One install = one site. The project_id partition column makes the
-- same code serve `scope: project` (one install per project, single
-- pid per row) and `scope: global` (one install across projects, the
-- agent/dashboard supply project_id explicitly).
--
-- Posts and pages share one table — `kind` distinguishes them. Page
-- hierarchy lives in posts.parent_id; menu order in posts.menu_order.
--
-- The body is `body_blocks` (JSON, canonical) plus `body_html`
-- (rendered cache, invalidated on update). Revisions snapshot the
-- full block tree.

CREATE TABLE posts (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id           TEXT    NOT NULL,
  kind                 TEXT    NOT NULL DEFAULT 'post',  -- 'post' | 'page'
  slug                 TEXT    NOT NULL,
  locale               TEXT    NOT NULL DEFAULT 'en',
  status               TEXT    NOT NULL DEFAULT 'draft', -- draft | scheduled | published | archived
  title                TEXT    NOT NULL DEFAULT '',
  excerpt              TEXT    NOT NULL DEFAULT '',
  body_blocks          TEXT    NOT NULL DEFAULT '{"version":1,"blocks":[]}',
  body_html            TEXT    NOT NULL DEFAULT '',
  author               TEXT    NOT NULL DEFAULT '',
  featured_media_id    INTEGER,
  parent_id            INTEGER,                          -- only meaningful when kind='page'
  menu_order           INTEGER NOT NULL DEFAULT 0,
  template             TEXT    NOT NULL DEFAULT '',     -- theme template override
  seo_title            TEXT    NOT NULL DEFAULT '',
  seo_description      TEXT    NOT NULL DEFAULT '',
  seo_canonical        TEXT    NOT NULL DEFAULT '',
  og_image_media_id    INTEGER,
  published_at         TIMESTAMP,
  scheduled_at         TIMESTAMP,
  created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at           TIMESTAMP,
  FOREIGN KEY (parent_id) REFERENCES posts(id) ON DELETE SET NULL,
  FOREIGN KEY (featured_media_id) REFERENCES media(id) ON DELETE SET NULL,
  FOREIGN KEY (og_image_media_id) REFERENCES media(id) ON DELETE SET NULL
);

-- One slug per (project, locale, kind). Pages and posts can share a
-- slug; categories of either can also share slugs since they live in
-- the terms table.
CREATE UNIQUE INDEX posts_slug_uniq
  ON posts (project_id, locale, kind, slug)
  WHERE deleted_at IS NULL;

CREATE INDEX posts_project_status_idx ON posts (project_id, status, published_at DESC);
CREATE INDEX posts_project_kind_idx ON posts (project_id, kind, published_at DESC);
CREATE INDEX posts_parent_idx ON posts (parent_id);

CREATE TABLE revisions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  post_id      INTEGER NOT NULL,
  body_blocks  TEXT    NOT NULL,                         -- snapshot
  title        TEXT    NOT NULL DEFAULT '',
  excerpt      TEXT    NOT NULL DEFAULT '',
  snapshot_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  author       TEXT    NOT NULL DEFAULT '',
  source       TEXT    NOT NULL DEFAULT 'human',         -- 'agent' | 'human' | 'import'
  note         TEXT    NOT NULL DEFAULT '',
  FOREIGN KEY (post_id) REFERENCES posts(id) ON DELETE CASCADE
);

CREATE INDEX revisions_post_idx ON revisions (post_id, snapshot_at DESC);

CREATE TABLE terms (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  kind        TEXT    NOT NULL,                          -- 'category' | 'tag'
  name        TEXT    NOT NULL,
  slug        TEXT    NOT NULL,
  parent_id   INTEGER,
  description TEXT    NOT NULL DEFAULT '',
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (parent_id) REFERENCES terms(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX terms_slug_uniq ON terms (project_id, kind, slug);
CREATE INDEX terms_parent_idx ON terms (parent_id);

CREATE TABLE post_terms (
  post_id  INTEGER NOT NULL,
  term_id  INTEGER NOT NULL,
  PRIMARY KEY (post_id, term_id),
  FOREIGN KEY (post_id) REFERENCES posts(id) ON DELETE CASCADE,
  FOREIGN KEY (term_id) REFERENCES terms(id) ON DELETE CASCADE
);

CREATE INDEX post_terms_term_idx ON post_terms (term_id);

CREATE TABLE media (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT    NOT NULL,
  kind          TEXT    NOT NULL DEFAULT 'file',          -- 'image' | 'video' | 'audio' | 'file'
  storage_path  TEXT    NOT NULL DEFAULT '',              -- /.media/<uuid>.<ext>
  filename      TEXT    NOT NULL DEFAULT '',
  mime          TEXT    NOT NULL DEFAULT '',
  width         INTEGER,
  height        INTEGER,
  byte_size     INTEGER NOT NULL DEFAULT 0,
  alt           TEXT    NOT NULL DEFAULT '',
  caption       TEXT    NOT NULL DEFAULT '',
  source        TEXT    NOT NULL DEFAULT 'upload',        -- 'upload' | 'image-studio' | 'url-import'
  uploaded_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX media_project_idx ON media (project_id, uploaded_at DESC);

CREATE TABLE menus (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  slug        TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX menus_slug_uniq ON menus (project_id, slug);

CREATE TABLE menu_items (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  menu_id      INTEGER NOT NULL,
  parent_id    INTEGER,
  label        TEXT    NOT NULL,
  target_kind  TEXT    NOT NULL DEFAULT 'url',            -- 'post' | 'page' | 'term' | 'url'
  target_id    INTEGER,
  target_url   TEXT    NOT NULL DEFAULT '',
  position     INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (menu_id) REFERENCES menus(id) ON DELETE CASCADE,
  FOREIGN KEY (parent_id) REFERENCES menu_items(id) ON DELETE CASCADE
);

CREATE INDEX menu_items_menu_idx ON menu_items (menu_id, position);

CREATE TABLE redirects (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  from_path   TEXT    NOT NULL,
  to_path     TEXT    NOT NULL,
  code        INTEGER NOT NULL DEFAULT 301,               -- 301 | 302
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX redirects_from_uniq ON redirects (project_id, from_path);

CREATE TABLE settings (
  project_id  TEXT    NOT NULL,
  key         TEXT    NOT NULL,
  value       TEXT    NOT NULL DEFAULT '',
  updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, key)
);

-- Audit only externally-observable state transitions (publish,
-- newsletter sent, cross-posted). Edits live in revisions, not here.
CREATE TABLE publish_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  post_id   INTEGER NOT NULL,
  event     TEXT    NOT NULL,                              -- published | scheduled | unpublished | archived | cross_posted | newsletter_sent
  at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  source    TEXT    NOT NULL DEFAULT '',
  metadata  TEXT    NOT NULL DEFAULT '{}',
  FOREIGN KEY (post_id) REFERENCES posts(id) ON DELETE CASCADE
);

CREATE INDEX publish_events_post_idx ON publish_events (post_id, at DESC);
