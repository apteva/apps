-- Creators v0.1.0 — Patreon-like membership, gated posts, and file drops.
-- Creators owns the membership domain model. Storage owns file bytes,
-- billing owns customer/invoice/payment records, CRM owns cross-app contacts.

CREATE TABLE IF NOT EXISTS creator_spaces (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT NOT NULL,
  name            TEXT NOT NULL DEFAULT 'Creator Space',
  slug            TEXT NOT NULL DEFAULT 'creator',
  description     TEXT NOT NULL DEFAULT '',
  avatar_file_id  INTEGER,
  banner_file_id  INTEGER,
  default_currency TEXT NOT NULL DEFAULT 'USD',
  metadata        TEXT NOT NULL DEFAULT '{}',
  created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_creator_spaces_slug
  ON creator_spaces(project_id, slug);

CREATE TABLE IF NOT EXISTS tiers (
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

CREATE INDEX IF NOT EXISTS ix_tiers_project ON tiers(project_id, space_id, archived_at, sort_order);

CREATE TABLE IF NOT EXISTS members (
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

CREATE INDEX IF NOT EXISTS ix_members_project_status ON members(project_id, space_id, status);
CREATE INDEX IF NOT EXISTS ix_members_project_tier ON members(project_id, space_id, tier_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_members_portal_token ON members(portal_token);

CREATE TABLE IF NOT EXISTS posts (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  space_id     INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  title        TEXT NOT NULL,
  slug         TEXT NOT NULL,
  body         TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'draft'
               CHECK(status IN ('draft','published','scheduled','archived')),
  visibility   TEXT NOT NULL DEFAULT 'members'
               CHECK(visibility IN ('public','members','tier','private')),
  tier_ids_json TEXT NOT NULL DEFAULT '[]',
  published_at TEXT,
  scheduled_at TEXT,
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(space_id, slug)
);

CREATE INDEX IF NOT EXISTS ix_posts_project_status ON posts(project_id, space_id, status, published_at);
CREATE INDEX IF NOT EXISTS ix_posts_project_visibility ON posts(project_id, space_id, visibility);

CREATE TABLE IF NOT EXISTS attachments (
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

CREATE INDEX IF NOT EXISTS ix_attachments_post ON attachments(post_id);
CREATE INDEX IF NOT EXISTS ix_attachments_file ON attachments(storage_file_id);

CREATE TABLE IF NOT EXISTS creator_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT NOT NULL,
  space_id    INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL,
  actor       TEXT NOT NULL DEFAULT 'system',
  subject_type TEXT NOT NULL DEFAULT '',
  subject_id INTEGER NOT NULL DEFAULT 0,
  data_json   TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_creator_events_project ON creator_events(project_id, space_id, id DESC);
