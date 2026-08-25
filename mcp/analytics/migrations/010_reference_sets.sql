-- Analytics v0.13 — project-scoped reference sets for governed dimensions.

CREATE TABLE reference_sets (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  key         TEXT    NOT NULL,
  label       TEXT    NOT NULL DEFAULT '',
  description TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE(project_id, key)
);

CREATE INDEX ix_reference_sets_project_key
ON reference_sets(project_id, key);

CREATE TABLE reference_values (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  reference_set_id INTEGER NOT NULL REFERENCES reference_sets(id) ON DELETE CASCADE,
  value            TEXT    NOT NULL,
  label            TEXT    NOT NULL DEFAULT '',
  status           TEXT    NOT NULL DEFAULT 'active',
  metadata_json    TEXT    NOT NULL DEFAULT '{}',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  UNIQUE(reference_set_id, value)
);

CREATE INDEX ix_reference_values_set_status_value
ON reference_values(reference_set_id, status, value);

ALTER TABLE event_property_specs ADD COLUMN reference_set TEXT;
