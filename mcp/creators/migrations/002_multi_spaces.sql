-- Creators v0.1.2 - support multiple creator spaces per project.
--
-- Existing v0.1.0/v0.1.1 installs had exactly one creator_space per project
-- and stored all domain rows directly under project_id. This migration moves
-- the space boundary into the domain tables while assigning existing rows to
-- the current/default space for their project.

PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE IF NOT EXISTS creator_spaces_v2 (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id       TEXT NOT NULL,
  name             TEXT NOT NULL DEFAULT 'Creator Space',
  slug             TEXT NOT NULL DEFAULT 'creator',
  description      TEXT NOT NULL DEFAULT '',
  avatar_file_id   INTEGER,
  banner_file_id   INTEGER,
  default_currency TEXT NOT NULL DEFAULT 'USD',
  metadata         TEXT NOT NULL DEFAULT '{}',
  created_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO creator_spaces_v2
  (id, project_id, name, slug, description, avatar_file_id, banner_file_id, default_currency, metadata, created_at, updated_at)
SELECT id, project_id, name, slug, description, avatar_file_id, banner_file_id, default_currency, metadata, created_at, updated_at
FROM creator_spaces;

DROP TABLE creator_spaces;
ALTER TABLE creator_spaces_v2 RENAME TO creator_spaces;

CREATE UNIQUE INDEX IF NOT EXISTS ux_creator_spaces_slug
  ON creator_spaces(project_id, slug);

CREATE TABLE IF NOT EXISTS tiers_v2 (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL,
  space_id      INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  slug          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  price_cents   INTEGER NOT NULL DEFAULT 0,
  currency      TEXT NOT NULL DEFAULT 'USD',
  interval      TEXT NOT NULL DEFAULT 'month',
  benefits_json TEXT NOT NULL DEFAULT '[]',
  sort_order    INTEGER NOT NULL DEFAULT 0,
  archived_at   TEXT,
  created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(space_id, slug)
);

INSERT INTO tiers_v2
  (id, project_id, space_id, name, slug, description, price_cents, currency, interval, benefits_json, sort_order, archived_at, created_at, updated_at)
SELECT t.id, t.project_id, cs.id, t.name, t.slug, t.description, t.price_cents, t.currency, t.interval, t.benefits_json, t.sort_order, t.archived_at, t.created_at, t.updated_at
FROM tiers t
JOIN creator_spaces cs ON cs.project_id = t.project_id
WHERE cs.id = (SELECT MIN(id) FROM creator_spaces WHERE project_id = t.project_id);

DROP TABLE tiers;
ALTER TABLE tiers_v2 RENAME TO tiers;
CREATE INDEX IF NOT EXISTS ix_tiers_project ON tiers(project_id, space_id, archived_at, sort_order);

CREATE TABLE IF NOT EXISTS members_v2 (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id           TEXT NOT NULL,
  space_id             INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  email                TEXT NOT NULL,
  display_name         TEXT NOT NULL DEFAULT '',
  status               TEXT NOT NULL DEFAULT 'lead'
                       CHECK(status IN ('lead','active','past_due','paused','cancelled','comped')),
  tier_id              INTEGER REFERENCES tiers(id) ON DELETE SET NULL,
  crm_contact_id       INTEGER,
  billing_customer_id  INTEGER,
  portal_token         TEXT NOT NULL,
  current_period_start TEXT,
  current_period_end   TEXT,
  metadata             TEXT NOT NULL DEFAULT '{}',
  created_at           TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(space_id, email)
);

INSERT INTO members_v2
  (id, project_id, space_id, email, display_name, status, tier_id, crm_contact_id, billing_customer_id, portal_token, current_period_start, current_period_end, metadata, created_at, updated_at)
SELECT m.id, m.project_id, cs.id, m.email, m.display_name, m.status, m.tier_id, m.crm_contact_id, m.billing_customer_id, m.portal_token, m.current_period_start, m.current_period_end, m.metadata, m.created_at, m.updated_at
FROM members m
JOIN creator_spaces cs ON cs.project_id = m.project_id
WHERE cs.id = (SELECT MIN(id) FROM creator_spaces WHERE project_id = m.project_id);

DROP TABLE members;
ALTER TABLE members_v2 RENAME TO members;
CREATE INDEX IF NOT EXISTS ix_members_project_status ON members(project_id, space_id, status);
CREATE INDEX IF NOT EXISTS ix_members_project_tier ON members(project_id, space_id, tier_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_members_portal_token ON members(portal_token);

CREATE TABLE IF NOT EXISTS posts_v2 (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL,
  space_id      INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  title         TEXT NOT NULL,
  slug          TEXT NOT NULL,
  body          TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'draft'
                CHECK(status IN ('draft','published','scheduled','archived')),
  visibility    TEXT NOT NULL DEFAULT 'members'
                CHECK(visibility IN ('public','members','tier','private')),
  tier_ids_json TEXT NOT NULL DEFAULT '[]',
  published_at  TEXT,
  scheduled_at  TEXT,
  created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(space_id, slug)
);

INSERT INTO posts_v2
  (id, project_id, space_id, title, slug, body, status, visibility, tier_ids_json, published_at, scheduled_at, created_at, updated_at)
SELECT p.id, p.project_id, cs.id, p.title, p.slug, p.body, p.status, p.visibility, p.tier_ids_json, p.published_at, p.scheduled_at, p.created_at, p.updated_at
FROM posts p
JOIN creator_spaces cs ON cs.project_id = p.project_id
WHERE cs.id = (SELECT MIN(id) FROM creator_spaces WHERE project_id = p.project_id);

DROP TABLE posts;
ALTER TABLE posts_v2 RENAME TO posts;
CREATE INDEX IF NOT EXISTS ix_posts_project_status ON posts(project_id, space_id, status, published_at);
CREATE INDEX IF NOT EXISTS ix_posts_project_visibility ON posts(project_id, space_id, visibility);

CREATE TABLE IF NOT EXISTS attachments_v2 (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT NOT NULL,
  space_id        INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  post_id         INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  storage_file_id INTEGER NOT NULL,
  filename        TEXT NOT NULL DEFAULT '',
  content_type    TEXT NOT NULL DEFAULT '',
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  visibility      TEXT NOT NULL DEFAULT 'inherit'
                  CHECK(visibility IN ('inherit','public','members','tier','private')),
  tier_ids_json   TEXT NOT NULL DEFAULT '[]',
  created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO attachments_v2
  (id, project_id, space_id, post_id, storage_file_id, filename, content_type, size_bytes, visibility, tier_ids_json, created_at)
SELECT a.id, a.project_id, p.space_id, a.post_id, a.storage_file_id, a.filename, a.content_type, a.size_bytes, a.visibility, a.tier_ids_json, a.created_at
FROM attachments a
JOIN posts p ON p.id = a.post_id;

DROP TABLE attachments;
ALTER TABLE attachments_v2 RENAME TO attachments;
CREATE INDEX IF NOT EXISTS ix_attachments_post ON attachments(post_id);
CREATE INDEX IF NOT EXISTS ix_attachments_file ON attachments(storage_file_id);

CREATE TABLE IF NOT EXISTS creator_events_v2 (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  space_id     INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,
  actor        TEXT NOT NULL DEFAULT 'system',
  subject_type TEXT NOT NULL DEFAULT '',
  subject_id   INTEGER NOT NULL DEFAULT 0,
  data_json    TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO creator_events_v2
  (id, project_id, space_id, kind, actor, subject_type, subject_id, data_json, created_at)
SELECT e.id, e.project_id, cs.id, e.kind, e.actor, e.subject_type, e.subject_id, e.data_json, e.created_at
FROM creator_events e
JOIN creator_spaces cs ON cs.project_id = e.project_id
WHERE cs.id = (SELECT MIN(id) FROM creator_spaces WHERE project_id = e.project_id);

DROP TABLE creator_events;
ALTER TABLE creator_events_v2 RENAME TO creator_events;
CREATE INDEX IF NOT EXISTS ix_creator_events_project ON creator_events(project_id, space_id, id DESC);

COMMIT;
PRAGMA foreign_keys = ON;
