-- Coaching authority is explicit and bound to one adviser media generation.
ALTER TABLE telephony_listener_sessions ADD COLUMN coaching INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telephony_listener_sessions ADD COLUMN target_peer_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE telephony_listener_sessions ADD COLUMN target_owner_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE telephony_listener_sessions ADD COLUMN target_browser_epoch TEXT NOT NULL DEFAULT '';
ALTER TABLE telephony_listener_audit ADD COLUMN mode TEXT NOT NULL DEFAULT 'listen';
CREATE TABLE telephony_coaching_audit (
 id TEXT PRIMARY KEY, call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 listener_audit_id TEXT NOT NULL, project_id TEXT NOT NULL, principal TEXT NOT NULL,
 started_at TEXT NOT NULL, ended_at TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX telephony_coaching_audit_call ON telephony_coaching_audit(project_id,call_id,started_at);
