-- Persistent SQL projections.
-- Definitions and queue state are project-scoped. Projection result rows live
-- in p_<id> tables created by the Tables app and are exposed read-only through
-- tables_query placeholders.

CREATE TABLE projection_definitions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT NOT NULL,
  name            TEXT NOT NULL,
  version         INTEGER NOT NULL,
  status          TEXT NOT NULL DEFAULT 'active',
  sql_text        TEXT NOT NULL,
  source_tables   TEXT NOT NULL,
  result_columns  TEXT NOT NULL,
  scope_columns   TEXT NOT NULL,
  result_table    TEXT NOT NULL UNIQUE,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, name, version),
  UNIQUE(project_id, name)
);

CREATE TABLE projection_sources (
  projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
  table_id      INTEGER NOT NULL REFERENCES tables_meta(id) ON DELETE CASCADE,
  PRIMARY KEY(projection_id, table_id)
);

CREATE TABLE projection_changes (
  change_id   INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT NOT NULL,
  table_id    INTEGER NOT NULL,
  row_id      INTEGER NOT NULL,
  operation   TEXT NOT NULL,
  old_values  TEXT,
  new_values  TEXT,
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX projection_changes_project_idx
  ON projection_changes(project_id, change_id);
CREATE INDEX projection_changes_table_idx
  ON projection_changes(table_id, change_id);

CREATE TABLE projection_cursors (
  projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL,
  last_change_id INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(projection_id, project_id)
);

CREATE TABLE projection_queue (
  projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL,
  scope_key     TEXT NOT NULL,
  pending_change_id INTEGER NOT NULL DEFAULT 0,
  claimed_until TIMESTAMP,
  attempts      INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT,
  queued_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(projection_id, project_id, scope_key)
);
CREATE INDEX projection_queue_ready_idx
  ON projection_queue(project_id, projection_id, claimed_until, queued_at);

CREATE TABLE projection_scopes (
  projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL,
  scope_key     TEXT NOT NULL,
  processed_change_id INTEGER NOT NULL DEFAULT 0,
  computed_at   TIMESTAMP,
  status        TEXT NOT NULL DEFAULT 'pending',
  last_error    TEXT,
  PRIMARY KEY(projection_id, project_id, scope_key)
);

CREATE TABLE projection_result_index (
  projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL,
  scope_key     TEXT NOT NULL,
  result_id     INTEGER NOT NULL,
  PRIMARY KEY(projection_id, project_id, scope_key, result_id)
);
CREATE INDEX projection_result_index_result_idx
  ON projection_result_index(projection_id, project_id, result_id);
