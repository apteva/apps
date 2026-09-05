CREATE TABLE experiments (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 name TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('paused','running','completed','failed')),
 state_json TEXT NOT NULL,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX experiments_project ON experiments(project_id, id DESC);
CREATE TABLE model_versions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 experiment_id INTEGER NOT NULL REFERENCES experiments(id),
 name TEXT NOT NULL,
 state_json TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX versions_project ON model_versions(project_id, id DESC);
CREATE TABLE deployments (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 version_id INTEGER NOT NULL REFERENCES model_versions(id),
 name TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX deployments_project ON deployments(project_id, id DESC);
