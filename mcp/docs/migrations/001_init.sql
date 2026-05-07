-- docs v0.1 — templates + render audit log.
--
-- templates: operator-authored markdown bodies with Go-template
-- placeholders. body is the source-of-truth string; everything else
-- is metadata. Slug is what agents reference instead of memorising
-- numeric ids — UNIQUE keeps namespace clean.
CREATE TABLE templates (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  slug            TEXT    NOT NULL UNIQUE,
  name            TEXT    NOT NULL,
  description     TEXT    NOT NULL DEFAULT '',
  body            TEXT    NOT NULL,
  -- source_format: 'markdown' is the only supported value today.
  -- Future 'html' would require a Chromium-backed renderer; we
  -- keep the column so adding it later doesn't break the schema.
  source_format   TEXT    NOT NULL DEFAULT 'markdown',
  -- output_format: 'pdf' for now. 'docx' would require a separate
  -- renderer; same forward-compat rationale.
  output_format   TEXT    NOT NULL DEFAULT 'pdf',
  -- variables JSON: optional declared schema {name, type, required}
  -- the panel uses to auto-generate the render form.
  variables_json  TEXT    NOT NULL DEFAULT '[]',
  -- default folder where renders of this template land in storage.
  -- Per-render output_folder arg overrides; install-level config
  -- overrides if this is empty.
  default_folder  TEXT    NOT NULL DEFAULT '',
  created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_templates_slug ON templates(slug);

-- renders: every successful render gets an audit row. data_snapshot
-- is the JSON of inputs at render time — load-bearing for compliance
-- use cases ("show me what data was on this invoice").
--
-- output_file_id is storage's id; we don't FK because it's in
-- storage's DB, not ours. If storage drops the file, the render row
-- still exists as audit (lookup just fails).
CREATE TABLE renders (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  template_id     INTEGER NOT NULL,
  -- template_slug snapshotted so post-rename we still know what
  -- template was used. (template_id stays valid until the template
  -- is hard-deleted.)
  template_slug   TEXT    NOT NULL,
  output_file_id  TEXT    NOT NULL,
  output_name     TEXT    NOT NULL DEFAULT '',
  output_folder   TEXT    NOT NULL DEFAULT '',
  data_snapshot   TEXT    NOT NULL,
  rendered_by     TEXT    NOT NULL DEFAULT '',
  rendered_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
  bytes           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_renders_template ON renders(template_id, rendered_at);
CREATE INDEX idx_renders_recent   ON renders(rendered_at);
