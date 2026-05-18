-- Templates — site-kit catalog. Bundled templates ship inside the
-- binary via //go:embed and get UPSERTed into this table at boot per
-- project; user-imported / agent-created templates land here too,
-- distinguished by `source`.
--
-- The full template definition lives in the `body` column (raw YAML);
-- the row's other columns are the parsed metadata header used by the
-- panel catalog and search. UPSERT keys on (project_id, name).
--
-- No applied-templates audit table: per the "match the analog" rule
-- (themes_set_active doesn't audit, posts_publish events go to
-- publish_events which is for externally-observable post transitions
-- only), the catalog is enough. Once we ship a "started from X"
-- UI affordance, we'll revisit.

CREATE TABLE templates (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT    NOT NULL,
  name          TEXT    NOT NULL,        -- 'finance-blog'
  display_name  TEXT    NOT NULL,
  version       TEXT    NOT NULL,
  description   TEXT    NOT NULL DEFAULT '',
  tags          TEXT    NOT NULL DEFAULT '[]',  -- JSON array
  preview_image TEXT    NOT NULL DEFAULT '',
  source        TEXT    NOT NULL DEFAULT 'bundled',  -- bundled | imported | agent
  body          TEXT    NOT NULL,         -- full YAML definition
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX templates_name_uniq ON templates (project_id, name);
CREATE INDEX templates_source_idx ON templates (project_id, source);
