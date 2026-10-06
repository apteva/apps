-- Durable, redacted read diagnostics for the Tables panel and dashboard widget.
CREATE TABLE IF NOT EXISTS read_diagnostics (
  id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  recorded_at_ms INTEGER NOT NULL,
  operation TEXT NOT NULL,
  call_id TEXT NOT NULL,
  request_id TEXT NOT NULL DEFAULT '',
  query_id TEXT NOT NULL,
  outcome TEXT NOT NULL,
  stage TEXT NOT NULL DEFAULT '',
  deadline_source TEXT NOT NULL DEFAULT '',
  total_ms INTEGER NOT NULL DEFAULT 0,
  sql_ms INTEGER NOT NULL DEFAULT 0,
  read_queue_ms INTEGER NOT NULL DEFAULT 0,
  select_ms INTEGER NOT NULL DEFAULT 0,
  scan_ms INTEGER NOT NULL DEFAULT 0,
  rows_returned INTEGER NOT NULL DEFAULT 0,
  rows_materialized INTEGER NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  error_type TEXT NOT NULL DEFAULT '',
  sqlite_error_code INTEGER
);
CREATE INDEX IF NOT EXISTS read_diagnostics_project_time
  ON read_diagnostics(project_id, recorded_at_ms DESC, id DESC);
