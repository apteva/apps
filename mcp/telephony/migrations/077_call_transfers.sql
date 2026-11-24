-- Call transfers: a connected call rings advisers' browsers, started by the
-- call's AI or by the adviser on the call. At most one rings per call.
CREATE TABLE IF NOT EXISTS call_transfers (
    id TEXT PRIMARY KEY,
    call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    source TEXT NOT NULL,                      -- ai | adviser | operator
    status TEXT NOT NULL,                      -- ringing | answered | no_answer | canceled | failed | abandoned
    target_kind TEXT NOT NULL,                 -- destination | ring_group
    target_id TEXT NOT NULL,
    target_name TEXT NOT NULL DEFAULT '',
    destinations_json TEXT NOT NULL DEFAULT '[]',
    from_principal TEXT NOT NULL DEFAULT '',
    from_destination_id TEXT NOT NULL DEFAULT '',
    from_agent_id INTEGER NOT NULL DEFAULT 0,
    from_thread_id TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    no_answer TEXT NOT NULL DEFAULT 'return',  -- return | hangup
    prior_hold_state TEXT NOT NULL DEFAULT 'active',
    started_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    ended_at TEXT NOT NULL DEFAULT '',
    answered_by TEXT NOT NULL DEFAULT '',
    answered_destination_id TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_call_transfers_ringing ON call_transfers(call_id) WHERE status='ringing';
CREATE INDEX IF NOT EXISTS idx_call_transfers_due ON call_transfers(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_call_transfers_call ON call_transfers(call_id, started_at);
-- Call lists are cached per read version, and a transfer changes what a call
-- offers to advisers.
CREATE TRIGGER IF NOT EXISTS telephony_read_call_transfers_insert AFTER INSERT ON call_transfers BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER IF NOT EXISTS telephony_read_call_transfers_update AFTER UPDATE OF status ON call_transfers WHEN NEW.status IS NOT OLD.status BEGIN
 INSERT INTO telephony_read_versions(project_id,revision) SELECT NEW.project_id,1 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
