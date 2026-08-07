-- Persist collector attempts separately from successful refreshes so worker
-- cadence and provider backoff survive app restarts.

ALTER TABLE ad_sync_state ADD COLUMN last_attempt_at TEXT NOT NULL DEFAULT '';
ALTER TABLE ad_sync_state ADD COLUMN last_success_at TEXT NOT NULL DEFAULT '';
ALTER TABLE ad_sync_state ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ad_sync_state ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT '';

UPDATE ad_sync_state
SET last_attempt_at = last_incremental_at,
    last_success_at = CASE WHEN last_status = 'ok' THEN last_incremental_at ELSE '' END;
