CREATE TABLE telephony_auto_answer_policies (
 project_id TEXT NOT NULL,
 scope TEXT NOT NULL,
 scope_id TEXT NOT NULL DEFAULT '',
 policy_json TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,scope,scope_id)
);
CREATE TABLE telephony_auto_answer_sessions (
 project_id TEXT NOT NULL,
 principal TEXT NOT NULL,
 session_id TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 PRIMARY KEY(project_id,principal)
);
CREATE INDEX idx_auto_answer_session_expiry ON telephony_auto_answer_sessions(expires_at);
CREATE TABLE telephony_auto_answer_reservations (
 project_id TEXT NOT NULL,
 principal TEXT NOT NULL,
 session_id TEXT NOT NULL,
 call_id TEXT NOT NULL UNIQUE,
 destination_id TEXT NOT NULL DEFAULT '',
 expires_at INTEGER NOT NULL,
 PRIMARY KEY(project_id,principal)
);
CREATE INDEX idx_auto_answer_reservation_expiry ON telephony_auto_answer_reservations(expires_at);
