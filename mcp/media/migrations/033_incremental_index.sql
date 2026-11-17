-- Incremental indexing. The 30 s indexer tick used to list every Storage
-- file and re-verify every media row on every run. It now drains a durable
-- queue fed by Storage's change feed (or SSE events on older Storage) and
-- advances a bounded reconciliation sweep whose progress is persisted here.

-- One row per file needing a look. Repeated events for the same file
-- collapse into one row (latest op wins, revision only moves forward).
CREATE TABLE index_queue (
  project_id  TEXT    NOT NULL,
  file_id     TEXT    NOT NULL,
  revision    INTEGER NOT NULL DEFAULT 0,
  op          TEXT    NOT NULL DEFAULT 'changed',  -- changed | deleted
  source      TEXT    NOT NULL DEFAULT '',         -- feed | event | sweep
  enqueued_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  gen         INTEGER NOT NULL DEFAULT 0,          -- bumped on every re-enqueue
  PRIMARY KEY (project_id, file_id)
);
CREATE INDEX ix_index_queue_order ON index_queue(project_id, enqueued_at);

-- Per-project sync cursors. project_id '*' holds the firehose SSE cursor
-- for global installs.
CREATE TABLE index_sync_state (
  project_id          TEXT PRIMARY KEY,
  feed_mode           TEXT    NOT NULL DEFAULT '',  -- '' unknown | feed | legacy
  feed_checked_at     TEXT,
  feed_after          INTEGER NOT NULL DEFAULT 0,   -- Storage /files/changes cursor
  event_seq           INTEGER NOT NULL DEFAULT 0,   -- platform SSE since=
  sweep_active        INTEGER NOT NULL DEFAULT 1,   -- initial discovery runs once
  sweep_cursor        INTEGER NOT NULL DEFAULT 0,   -- after_id (feed) or offset (legacy)
  sweep_started_at    TEXT,
  sweep_completed_at  TEXT,
  orphan_cursor       TEXT    NOT NULL DEFAULT ''   -- media file_id resume point
);
