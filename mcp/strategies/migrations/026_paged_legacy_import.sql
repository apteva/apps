CREATE TABLE legacy_import_progress (
 source_install_id INTEGER NOT NULL, project_id TEXT NOT NULL,
 manifest_json TEXT NOT NULL, table_index INTEGER NOT NULL DEFAULT 0,
 row_cursor INTEGER NOT NULL DEFAULT 0, rows_imported INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(source_install_id, project_id)
);
