-- Retry failed reservation releases without modifying files or live sessions.
CREATE TABLE upload_reservation_cleanup (
  upload_id TEXT PRIMARY KEY,
  requested_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
