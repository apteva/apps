-- Analytics v0.8 — event specs, properties, validation, and upsertable
-- aggregate-observation events.

ALTER TABLE events ADD COLUMN upsert_key TEXT;

CREATE UNIQUE INDEX ux_events_project_app_topic_upsert
ON events(project_id, app, topic, upsert_key)
WHERE upsert_key IS NOT NULL;

CREATE TABLE event_specs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT    NOT NULL,
  app             TEXT    NOT NULL,
  topic           TEXT    NOT NULL,
  kind            TEXT    NOT NULL DEFAULT 'occurrence',
  display_name    TEXT    NOT NULL DEFAULT '',
  description     TEXT    NOT NULL DEFAULT '',
  category        TEXT    NOT NULL DEFAULT '',
  status          TEXT    NOT NULL DEFAULT 'active',
  validation_mode TEXT    NOT NULL DEFAULT 'observe',
  ingest_mode     TEXT    NOT NULL DEFAULT 'raw',
  upsert_policy   TEXT,
  rollup_policy   TEXT,
  created_by      TEXT    NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  UNIQUE(project_id, app, topic)
);

CREATE INDEX ix_event_specs_project_app ON event_specs(project_id, app, topic);

CREATE TABLE event_property_specs (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  event_spec_id      INTEGER NOT NULL REFERENCES event_specs(id) ON DELETE CASCADE,
  key                TEXT    NOT NULL,
  type               TEXT    NOT NULL DEFAULT 'string',
  required           INTEGER NOT NULL DEFAULT 0,
  description        TEXT    NOT NULL DEFAULT '',
  enum_values        TEXT,
  pii_classification TEXT    NOT NULL DEFAULT 'none',
  example_value      TEXT,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  UNIQUE(event_spec_id, key)
);

CREATE INDEX ix_event_property_specs_spec ON event_property_specs(event_spec_id);

CREATE TABLE event_spec_violations (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT    NOT NULL,
  app            TEXT    NOT NULL,
  topic          TEXT    NOT NULL,
  event_id       INTEGER,
  violation_type TEXT    NOT NULL,
  message        TEXT    NOT NULL,
  property_key   TEXT    NOT NULL DEFAULT '',
  seen_at        INTEGER NOT NULL
);

CREATE INDEX ix_event_spec_violations_lookup
ON event_spec_violations(project_id, app, topic, seen_at);
