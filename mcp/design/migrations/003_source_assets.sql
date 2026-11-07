CREATE TABLE source_assets (
 project_id TEXT NOT NULL,
 id TEXT NOT NULL,
 metadata_json TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(project_id,id)
);
