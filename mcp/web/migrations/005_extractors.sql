CREATE TABLE web_extractors (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT,
  enabled         INTEGER NOT NULL DEFAULT 1,
  revision        INTEGER NOT NULL DEFAULT 1,
  definition_json TEXT NOT NULL,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_web_extractors_project_name
  ON web_extractors(project_id, name);

ALTER TABLE web_runs ADD COLUMN extractor_id INTEGER;
ALTER TABLE web_runs ADD COLUMN extractor_revision INTEGER;
ALTER TABLE web_runs ADD COLUMN definition_snapshot_json TEXT;
ALTER TABLE web_runs ADD COLUMN trigger_json TEXT;
ALTER TABLE web_runs ADD COLUMN cancel_requested_at TIMESTAMP;

CREATE INDEX ix_web_runs_extractor_created
  ON web_runs(project_id, extractor_id, created_at DESC);

CREATE INDEX ix_web_runs_queue
  ON web_runs(status, created_at)
  WHERE status = 'queued';

CREATE UNIQUE INDEX ux_web_runs_trigger_key
  ON web_runs(project_id, json_extract(trigger_json, '$.trigger_key'))
  WHERE json_extract(trigger_json, '$.trigger_key') IS NOT NULL;
