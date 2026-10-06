PRAGMA foreign_keys = ON;
CREATE TABLE studio_assets (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 name TEXT NOT NULL,
 head_revision_id INTEGER,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX studio_assets_project ON studio_assets(project_id, id);
CREATE TABLE studio_revisions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 asset_id INTEGER NOT NULL REFERENCES studio_assets(id),
 parent_revision_id INTEGER REFERENCES studio_revisions(id),
 document_json TEXT NOT NULL,
 commands_json TEXT NOT NULL,
 source_hash TEXT NOT NULL,
 note TEXT NOT NULL DEFAULT '',
 request_key TEXT,
 request_hash TEXT,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(asset_id, request_key)
);
CREATE INDEX studio_revisions_asset ON studio_revisions(asset_id, id);
CREATE TABLE studio_selections (
 id TEXT PRIMARY KEY,
 asset_id INTEGER NOT NULL REFERENCES studio_assets(id),
 revision_id INTEGER NOT NULL REFERENCES studio_revisions(id),
 name TEXT NOT NULL,
 selection_json TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE studio_candidates (
 id TEXT PRIMARY KEY,
 asset_id INTEGER NOT NULL REFERENCES studio_assets(id),
 base_revision_id INTEGER NOT NULL REFERENCES studio_revisions(id),
 result_json TEXT NOT NULL,
 commands_json TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE TABLE studio_artifacts (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 asset_id INTEGER NOT NULL REFERENCES studio_assets(id),
 revision_id INTEGER NOT NULL REFERENCES studio_revisions(id),
 candidate_id TEXT NOT NULL DEFAULT '',
 format TEXT NOT NULL,
 sha256 TEXT NOT NULL,
 content BLOB NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX studio_artifacts_asset ON studio_artifacts(asset_id, id);
