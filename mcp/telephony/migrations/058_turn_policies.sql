-- Identity keys include issuer, installation, subject and organization.
-- Temporary TURN credentials are never persisted.
CREATE TABLE telephony_turn_policies (
 project_id TEXT NOT NULL,
 principal TEXT NOT NULL DEFAULT '',
 policy_json TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,principal)
);
