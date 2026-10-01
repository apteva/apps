CREATE TABLE IF NOT EXISTS actors_crawl_queue (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT NOT NULL,
  run_id         INTEGER NOT NULL REFERENCES actors_runs(id) ON DELETE CASCADE,
  url            TEXT NOT NULL,
  route          TEXT NOT NULL,
  depth          INTEGER NOT NULL DEFAULT 0,
  parent_key     TEXT,
  dedupe_key     TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'pending',
  attempts       INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMP,
  lease_until    TIMESTAMP,
  fence          INTEGER NOT NULL DEFAULT 0,
  last_error     TEXT,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_actors_crawl_queue_run_key
  ON actors_crawl_queue(project_id, run_id, dedupe_key);
CREATE INDEX IF NOT EXISTS ix_actors_crawl_queue_claim
  ON actors_crawl_queue(project_id, run_id, status, next_attempt_at, id);

CREATE TABLE IF NOT EXISTS actors_crawl_records (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT NOT NULL,
  run_id      INTEGER NOT NULL REFERENCES actors_runs(id) ON DELETE CASCADE,
  dataset     TEXT NOT NULL,
  record_key  TEXT NOT NULL,
  source_url  TEXT,
  item_json   TEXT NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_actors_crawl_records_run_key
  ON actors_crawl_records(project_id, run_id, dataset, record_key);
CREATE INDEX IF NOT EXISTS ix_actors_crawl_records_read
  ON actors_crawl_records(project_id, run_id, dataset, id);

CREATE TABLE IF NOT EXISTS actors_crawl_materialized (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL,
  actor_id      INTEGER NOT NULL,
  dataset       TEXT NOT NULL,
  record_key    TEXT NOT NULL,
  item_json     TEXT NOT NULL,
  source_url    TEXT,
  last_run_id   INTEGER REFERENCES actors_runs(id) ON DELETE SET NULL,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_actors_crawl_materialized_key
  ON actors_crawl_materialized(project_id, actor_id, dataset, record_key);
CREATE INDEX IF NOT EXISTS ix_actors_crawl_materialized_read
  ON actors_crawl_materialized(project_id, dataset, id);
