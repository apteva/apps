-- Add `apibay` to the allowed indexer kinds.
--
-- SQLite can't ALTER a CHECK constraint in place, so the table is
-- rebuilt: copy rows into a new table with the widened CHECK, drop
-- the old, rename the new. Same column set + UNIQUE + INDEX as
-- 001_init.sql.

CREATE TABLE indexers_new (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id              TEXT    NOT NULL,
    name                    TEXT    NOT NULL,
    kind                    TEXT    NOT NULL DEFAULT 'jackett'
                            CHECK(kind IN ('jackett','prowlarr','rss','apibay')),
    base_url                TEXT    NOT NULL,
    api_key_enc             TEXT    NOT NULL DEFAULT '',
    categories_json         TEXT    NOT NULL DEFAULT '[]',
    priority                INTEGER NOT NULL DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    last_ok_at              TEXT,
    last_error              TEXT    NOT NULL DEFAULT '',
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

INSERT INTO indexers_new (id, project_id, name, kind, base_url, api_key_enc,
                          categories_json, priority, enabled, last_ok_at,
                          last_error, created_at)
SELECT id, project_id, name, kind, base_url, api_key_enc,
       categories_json, priority, enabled, last_ok_at,
       last_error, created_at
  FROM indexers;

DROP TABLE indexers;
ALTER TABLE indexers_new RENAME TO indexers;

CREATE INDEX IF NOT EXISTS idx_indexers_enabled ON indexers(project_id, enabled, priority);
