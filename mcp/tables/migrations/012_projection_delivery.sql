-- Durable, coalesced projection.ready delivery. Publication and enqueue are
-- committed together, so a sidecar restart cannot lose a ready notification.
CREATE TABLE projection_event_outbox (
  event_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
  topic TEXT NOT NULL,
  payload TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_ms INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at_ms INTEGER NOT NULL
);

CREATE INDEX projection_event_outbox_due_idx
  ON projection_event_outbox(next_attempt_ms, created_at_ms);

-- Older queue rows predate the integer timestamp used by queue_ms. Backfill
-- them once; new writes populate queued_at_ms directly.
UPDATE projection_queue
SET queued_at_ms=CAST(strftime('%s',queued_at) AS INTEGER)*1000
WHERE queued_at_ms=0 AND queued_at IS NOT NULL;
