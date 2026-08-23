CREATE TABLE IF NOT EXISTS pcb_designs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  current_revision_id INTEGER,
  archived INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pcb_designs_project ON pcb_designs(project_id, archived, updated_at DESC);

CREATE TABLE IF NOT EXISTS pcb_revisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  design_id INTEGER NOT NULL,
  project_id TEXT NOT NULL,
  parent_id INTEGER,
  number INTEGER NOT NULL,
  schema_version TEXT NOT NULL,
  definition_json TEXT NOT NULL,
  operations_json TEXT,
  source_sha256 TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(design_id, number),
  FOREIGN KEY(design_id) REFERENCES pcb_designs(id)
);

CREATE INDEX IF NOT EXISTS idx_pcb_revisions_design ON pcb_revisions(project_id, design_id, number DESC);

CREATE TABLE IF NOT EXISTS pcb_validation_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  design_id INTEGER NOT NULL,
  revision_id INTEGER NOT NULL,
  project_id TEXT NOT NULL,
  status TEXT NOT NULL,
  errors INTEGER NOT NULL,
  warnings INTEGER NOT NULL,
  report_json TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pcb_validation_revision ON pcb_validation_runs(project_id, revision_id, id DESC);

CREATE TABLE IF NOT EXISTS pcb_artifacts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  design_id INTEGER NOT NULL,
  revision_id INTEGER NOT NULL,
  project_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  format TEXT NOT NULL,
  name TEXT NOT NULL,
  content_type TEXT NOT NULL,
  local_path TEXT NOT NULL,
  storage_file_id TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pcb_artifacts_design ON pcb_artifacts(project_id, design_id, id DESC);
