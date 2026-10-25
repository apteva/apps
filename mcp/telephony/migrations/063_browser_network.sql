CREATE TABLE telephony_network_policies (
 project_id TEXT NOT NULL,
 principal TEXT NOT NULL DEFAULT '',
 policy_json TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,principal)
);
