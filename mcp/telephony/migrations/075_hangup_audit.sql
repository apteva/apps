-- One row per accepted hang-up or reject request, including requests that
-- arrived after the call had already ended.
CREATE TABLE telephony_hangup_audit (
 id TEXT PRIMARY KEY, call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, initiator TEXT NOT NULL, principal TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL, request_id TEXT NOT NULL DEFAULT '', requested_at TEXT NOT NULL,
 previous_status TEXT NOT NULL, outcome TEXT NOT NULL,
 termination_initiator TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX telephony_hangup_audit_call ON telephony_hangup_audit(project_id,call_id,requested_at);
