-- Independent credentials: never replace operator media grants or call ownership.
CREATE TABLE telephony_listener_sessions (
 token_hash TEXT PRIMARY KEY, call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, principal TEXT NOT NULL, provider_json TEXT NOT NULL DEFAULT '',
 expires_at INTEGER NOT NULL
);
CREATE INDEX telephony_listener_sessions_call ON telephony_listener_sessions(project_id,call_id,expires_at);
CREATE TABLE telephony_listener_audit (
 id TEXT PRIMARY KEY, call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, principal TEXT NOT NULL, joined_at TEXT NOT NULL,
 left_at TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', diagnostics_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX telephony_listener_audit_call ON telephony_listener_audit(project_id,call_id,joined_at);
