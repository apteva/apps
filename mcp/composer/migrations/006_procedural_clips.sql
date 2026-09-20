-- Additive procedural clip resources. Existing compositions and renders are unchanged.
CREATE TABLE procedures (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  latest_revision INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_procedures_project ON procedures(project_id, id DESC);

CREATE TABLE procedure_revisions (
  procedure_id INTEGER NOT NULL REFERENCES procedures(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL,
  runtime TEXT NOT NULL,
  entrypoint TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT 'clip',
  output_kind TEXT NOT NULL DEFAULT 'video',
  manifest_json TEXT NOT NULL DEFAULT '{}',
  files_json TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(procedure_id, revision)
);

CREATE TABLE procedure_materializations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  procedure_id INTEGER NOT NULL REFERENCES procedures(id) ON DELETE CASCADE,
  procedure_revision INTEGER NOT NULL,
  cache_key TEXT NOT NULL,
  status TEXT NOT NULL,
  storage_id INTEGER NOT NULL DEFAULT 0,
  artifact_kind TEXT NOT NULL DEFAULT '',
  duration_seconds REAL NOT NULL DEFAULT 0,
  runtime_job_id TEXT NOT NULL DEFAULT '',
  logs TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at TIMESTAMP,
  UNIQUE(project_id, cache_key)
);
CREATE INDEX idx_procedure_materializations_procedure
  ON procedure_materializations(project_id, procedure_id, id DESC);
