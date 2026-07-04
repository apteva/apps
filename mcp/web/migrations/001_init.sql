CREATE TABLE web_runs (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  kind          TEXT NOT NULL,
  input_json    TEXT NOT NULL,
  output_json   TEXT,
  status        TEXT NOT NULL DEFAULT 'running',
  error         TEXT,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at  TIMESTAMP
);
CREATE INDEX ix_web_runs_project_created
  ON web_runs(project_id, created_at DESC);

CREATE TABLE web_artifacts (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  run_id        INTEGER REFERENCES web_runs(id) ON DELETE SET NULL,
  kind          TEXT NOT NULL,
  url           TEXT,
  title         TEXT,
  storage_id    INTEGER,
  storage_url   TEXT,
  content_type  TEXT,
  bytes         INTEGER,
  metadata_json TEXT,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_web_artifacts_project_created
  ON web_artifacts(project_id, created_at DESC);
