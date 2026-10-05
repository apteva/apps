-- Allow multiple immutable definitions for one logical projection name while
-- keeping exactly one active reader version.
PRAGMA foreign_keys=OFF;
CREATE TABLE projection_definitions_v2 (
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
  UNIQUE(project_id, name, version)
);
INSERT INTO projection_definitions_v2
  (id,project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table,created_at,updated_at)
SELECT id,project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table,created_at,updated_at
FROM projection_definitions;
DROP TABLE projection_definitions;
ALTER TABLE projection_definitions_v2 RENAME TO projection_definitions;
CREATE UNIQUE INDEX projection_active_name_idx
  ON projection_definitions(project_id,name) WHERE status='active';
PRAGMA foreign_keys=ON;
