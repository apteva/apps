ALTER TABLE renders ADD COLUMN phase TEXT NOT NULL DEFAULT '';
ALTER TABLE renders ADD COLUMN progress_pct REAL NOT NULL DEFAULT 0;
ALTER TABLE renders ADD COLUMN progress_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE renders ADD COLUMN output_snapshot TEXT NOT NULL DEFAULT '{}';
ALTER TABLE renders ADD COLUMN next_attempt_at TIMESTAMP;
ALTER TABLE renders ADD COLUMN started_at TIMESTAMP;
ALTER TABLE renders ADD COLUMN finished_at TIMESTAMP;

CREATE INDEX idx_renders_queue
  ON renders(status, next_attempt_at, id)
  WHERE status = 'queued';
