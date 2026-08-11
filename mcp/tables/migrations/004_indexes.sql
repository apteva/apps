-- Logical index metadata keeps physical SQLite names private and lets agents
-- create safe composite indexes without accepting arbitrary SQL expressions.
CREATE TABLE indexes_meta (
  id            INTEGER PRIMARY KEY,
  table_id      INTEGER NOT NULL REFERENCES tables_meta(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  physical_name TEXT    NOT NULL UNIQUE,
  unique_index  INTEGER NOT NULL DEFAULT 0,
  managed       INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(table_id, name)
);

CREATE TABLE index_columns (
  index_id    INTEGER NOT NULL REFERENCES indexes_meta(id) ON DELETE CASCADE,
  column_name TEXT    NOT NULL,
  direction   TEXT    NOT NULL DEFAULT 'asc',
  position    INTEGER NOT NULL,
  PRIMARY KEY(index_id, position),
  UNIQUE(index_id, column_name)
);

CREATE INDEX ix_indexes_meta_table ON indexes_meta(table_id, name);
CREATE INDEX ix_index_columns_index ON index_columns(index_id, position);
