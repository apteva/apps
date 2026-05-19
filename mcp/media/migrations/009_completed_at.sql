-- 009_completed_at.sql
--
-- Tracks whether the "media.completed" event has been emitted for a
-- row. The column lets maybeEmitMediaCompleted stay idempotent across
-- sidecar restarts + cross-stage call sites (indexer / transcriber /
-- describer all invoke it; whichever runs last actually fires the
-- event, and the others early-return on the non-NULL check).
--
-- NULL = not yet emitted; a TIMESTAMP value = emitted at that
-- instant. Future re-emits (e.g. operator re-runs media_reindex)
-- intentionally do NOT reset this column — once emitted, downstream
-- agents have already processed the file, and a re-emit would
-- double-fire. Operators who want a fresh emit can UPDATE this
-- column to NULL by hand.

ALTER TABLE media ADD COLUMN completed_at TIMESTAMP;

-- Helps the maybeEmitMediaCompleted hot-path's pre-check (skip rows
-- already complete) on installs with many media rows. Partial index
-- only covers the not-yet-complete subset so the index stays small.
CREATE INDEX IF NOT EXISTS ix_media_completed_pending
  ON media (project_id, file_id)
  WHERE completed_at IS NULL;
