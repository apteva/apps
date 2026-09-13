CREATE TABLE processes (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','active','paused','archived')),
 current_version INTEGER NOT NULL DEFAULT 1,
 sync_pending INTEGER NOT NULL DEFAULT 0,
 sync_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX processes_project ON processes(project_id,updated_at DESC);
CREATE TABLE process_versions (
 process_id TEXT NOT NULL REFERENCES processes(id),
 version INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 created_by TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(process_id,version)
);
CREATE TABLE process_runs (
 id TEXT PRIMARY KEY,
 process_id TEXT NOT NULL,
 version INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('manual','schedule')),
 request_key TEXT NOT NULL,
 inputs TEXT NOT NULL DEFAULT '',
 task_id TEXT NOT NULL DEFAULT '',
 delivery_warning TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 FOREIGN KEY(process_id,version) REFERENCES process_versions(process_id,version),
 UNIQUE(process_id,request_key)
);
CREATE INDEX process_runs_history ON process_runs(process_id,created_at DESC);
