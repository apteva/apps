-- Patreon-style collections: ordered groups of posts with post-level access.

CREATE TABLE IF NOT EXISTS collections (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id            TEXT NOT NULL,
  space_id              INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  title                 TEXT NOT NULL,
  slug                  TEXT NOT NULL,
  description           TEXT NOT NULL DEFAULT '',
  status                TEXT NOT NULL DEFAULT 'draft'
                        CHECK(status IN ('draft','published','archived')),
  cover_storage_file_id INTEGER,
  metadata              TEXT NOT NULL DEFAULT '{}',
  sort_order            INTEGER NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(space_id, slug)
);

CREATE INDEX IF NOT EXISTS ix_collections_space
  ON collections(project_id, space_id, status, sort_order, id);

CREATE TABLE IF NOT EXISTS collection_posts (
  project_id    TEXT NOT NULL,
  space_id      INTEGER NOT NULL REFERENCES creator_spaces(id) ON DELETE CASCADE,
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  post_id       INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(collection_id, post_id)
);

CREATE INDEX IF NOT EXISTS ix_collection_posts_order
  ON collection_posts(project_id, space_id, collection_id, position, post_id);

CREATE INDEX IF NOT EXISTS ix_collection_posts_post
  ON collection_posts(project_id, space_id, post_id, collection_id);
