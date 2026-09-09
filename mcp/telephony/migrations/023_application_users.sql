-- Policies are installation-local and project-scoped. Identity is issuer-namespaced.
CREATE TABLE telephony_access_policies (
 project_id TEXT PRIMARY KEY, revision INTEGER NOT NULL, policy_json TEXT NOT NULL
);
CREATE TABLE telephony_call_owners (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, principal TEXT NOT NULL, destination_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX telephony_call_owners_principal ON telephony_call_owners(project_id,principal);
CREATE TABLE telephony_media_sessions (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, token_hash TEXT NOT NULL, principal TEXT NOT NULL,
 policy_revision INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
