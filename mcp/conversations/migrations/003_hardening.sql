-- conversations v0.8: tenant isolation, atomic approvals, and retryable outbox.

-- A global conversations install serves many projects. External identities are
-- unique inside a project, never across the whole installation.
DROP INDEX IF EXISTS idx_conversations_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_project_key
    ON conversations(project_id, conversation_key)
    WHERE conversation_key != '';

-- Explicit state makes approval resolution compare-and-swap safe. Existing
-- cards are conservatively treated as pending only when their JSON still says
-- pending; everything else is already resolved.
ALTER TABLE messages ADD COLUMN action_status TEXT NOT NULL DEFAULT '';
UPDATE messages
SET action_status = CASE
    WHEN component_kind = 'approval' AND components_json LIKE '%"status":"pending"%' THEN 'pending'
    WHEN component_kind = 'approval' THEN 'resolved'
    ELSE ''
END;
CREATE INDEX IF NOT EXISTS idx_messages_pending_approval
    ON messages(action_status, id)
    WHERE component_kind = 'approval' AND action_status = 'pending';

-- Retry scheduling is persisted so normal network failures survive process
-- restarts and do not hot-loop.
-- SQLite only accepts constant defaults in ALTER TABLE ADD COLUMN. Backfill
-- timestamps explicitly, while new deliveries get their schedule from the
-- insert/update paths in Go.
ALTER TABLE deliveries ADD COLUMN next_attempt_at DATETIME NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN updated_at DATETIME NOT NULL DEFAULT '';
UPDATE deliveries
SET next_attempt_at = COALESCE(NULLIF(next_attempt_at, ''), created_at),
    updated_at = COALESCE(NULLIF(updated_at, ''), created_at);
CREATE INDEX IF NOT EXISTS idx_deliveries_retry
    ON deliveries(status, next_attempt_at, id)
    WHERE status = 'pending';
