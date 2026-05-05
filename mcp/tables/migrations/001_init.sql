-- Tables v0.1 — typed-row database app.
--
-- Two metadata tables describe user-defined tables. The user-tables
-- themselves are physical sqlite tables created at runtime, named
-- t_<id> so that renames stay metadata-only. Column names inside a
-- physical table are kept in sync with col.name on rename.
--
-- Identifier validation lives in the Go layer; sqlite is happy to
-- accept anything quoted, but we restrict user-supplied names to
-- [a-z_][a-z0-9_]* to keep generated SQL safe and predictable.

CREATE TABLE tables_meta (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  scope         TEXT    NOT NULL DEFAULT 'project',  -- 'project' | 'global'
  name          TEXT    NOT NULL,
  physical_name TEXT    NOT NULL UNIQUE,             -- 't_<id>' — generated; never user-visible
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, name)
);

-- One row per user-defined column.
--
-- type is one of: text, number, bool, datetime, json, file_id.
-- default_value is stored as a JSON-encoded literal so we round-trip
-- types cleanly (NULL / "string" / 42 / true / {"a":1}). Position
-- preserves declaration order for tables_describe + the dashboard.
CREATE TABLE columns_meta (
  id            INTEGER PRIMARY KEY,
  table_id      INTEGER NOT NULL REFERENCES tables_meta(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  type          TEXT    NOT NULL,
  nullable      INTEGER NOT NULL DEFAULT 1,
  default_value TEXT,
  position      INTEGER NOT NULL,
  UNIQUE(table_id, name)
);

CREATE INDEX ix_tables_meta_proj  ON tables_meta(project_id, scope);
CREATE INDEX ix_columns_meta_tbl  ON columns_meta(table_id, position);
