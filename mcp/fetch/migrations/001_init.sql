CREATE TABLE fetch_saved_requests (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  slug            TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT,
  method          TEXT NOT NULL,
  url_template    TEXT NOT NULL,
  headers_json    TEXT,
  query_json      TEXT,
  body_json       TEXT,
  environment_id  INTEGER,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at     TIMESTAMP
);
CREATE UNIQUE INDEX ux_fetch_saved_requests_slug
  ON fetch_saved_requests(project_id, slug);

CREATE TABLE fetch_environments (
  id          INTEGER PRIMARY KEY,
  project_id  TEXT NOT NULL,
  slug        TEXT NOT NULL,
  name        TEXT NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at TIMESTAMP
);
CREATE UNIQUE INDEX ux_fetch_environments_slug
  ON fetch_environments(project_id, slug);

CREATE TABLE fetch_environment_vars (
  id             INTEGER PRIMARY KEY,
  project_id     TEXT NOT NULL,
  environment_id INTEGER NOT NULL REFERENCES fetch_environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  value          TEXT,
  is_secret      INTEGER NOT NULL DEFAULT 0,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(environment_id, key)
);

CREATE TABLE fetch_history (
  id                     INTEGER PRIMARY KEY,
  project_id             TEXT NOT NULL,
  saved_request_id        INTEGER,
  source                 TEXT,
  method                 TEXT NOT NULL,
  url                    TEXT NOT NULL,
  redacted_request_json   TEXT,
  status                 INTEGER,
  redacted_response_json  TEXT,
  duration_ms            INTEGER,
  error                  TEXT,
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_fetch_history_project_created
  ON fetch_history(project_id, created_at DESC);
