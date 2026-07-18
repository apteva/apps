-- docs v0.4 - HTML/CSS templates and reproducible render revisions.

ALTER TABLE templates ADD COLUMN stylesheet TEXT NOT NULL DEFAULT '';
ALTER TABLE templates ADD COLUMN settings_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE template_revisions (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  template_id       INTEGER NOT NULL,
  revision_number   INTEGER NOT NULL,
  source_format     TEXT    NOT NULL,
  body              TEXT    NOT NULL,
  stylesheet        TEXT    NOT NULL DEFAULT '',
  settings_json     TEXT    NOT NULL DEFAULT '{}',
  source_hash       TEXT    NOT NULL,
  created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
  -- Revisions intentionally outlive a deleted template so render audits remain reproducible.
  UNIQUE(template_id, revision_number)
);

CREATE INDEX idx_template_revisions_latest
  ON template_revisions(template_id, revision_number DESC);

ALTER TABLE renders ADD COLUMN template_revision_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE renders ADD COLUMN source_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE renders ADD COLUMN renderer_version TEXT NOT NULL DEFAULT '';
