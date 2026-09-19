CREATE TABLE actors_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL,
  kind          TEXT NOT NULL,
  input_json    TEXT NOT NULL,
  output_json   TEXT,
  status        TEXT NOT NULL DEFAULT 'running',
  error         TEXT,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at  TIMESTAMP
);
CREATE INDEX ix_actors_runs_project_created
  ON actors_runs(project_id, created_at DESC);

CREATE TABLE actors_artifacts (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL,
  run_id        INTEGER REFERENCES actors_runs(id) ON DELETE SET NULL,
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
CREATE INDEX ix_actors_artifacts_project_created
  ON actors_artifacts(project_id, created_at DESC);

ALTER TABLE actors_runs ADD COLUMN summary TEXT;


CREATE INDEX IF NOT EXISTS ix_actors_runs_status
  ON actors_runs(status);

CREATE INDEX IF NOT EXISTS ix_actors_runs_created
  ON actors_runs(created_at);

CREATE INDEX IF NOT EXISTS ix_actors_artifacts_created
  ON actors_artifacts(created_at);

CREATE TABLE actors_definitions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT,
  enabled         INTEGER NOT NULL DEFAULT 1,
  revision        INTEGER NOT NULL DEFAULT 1,
  definition_json TEXT NOT NULL,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_actors_definitions_project_name
  ON actors_definitions(project_id, name);

ALTER TABLE actors_runs ADD COLUMN actor_id INTEGER;
ALTER TABLE actors_runs ADD COLUMN actor_revision INTEGER;
ALTER TABLE actors_runs ADD COLUMN definition_snapshot_json TEXT;
ALTER TABLE actors_runs ADD COLUMN trigger_json TEXT;
ALTER TABLE actors_runs ADD COLUMN cancel_requested_at TIMESTAMP;

CREATE INDEX ix_actors_runs_actor_created
  ON actors_runs(project_id, actor_id, created_at DESC);

CREATE INDEX ix_actors_runs_queue
  ON actors_runs(status, created_at)
  WHERE status = 'queued';

CREATE UNIQUE INDEX ux_actors_runs_trigger_key
  ON actors_runs(project_id, json_extract(trigger_json, '$.trigger_key'))
  WHERE json_extract(trigger_json, '$.trigger_key') IS NOT NULL;
