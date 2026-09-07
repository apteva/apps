CREATE TABLE build_attestations (
 build_id INTEGER PRIMARY KEY REFERENCES builds(id) ON DELETE CASCADE,
 artifact_sha256 TEXT NOT NULL,
 manifest_json TEXT NOT NULL,
 target_json TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE release_workflows (
 release_id INTEGER PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
 config_json TEXT NOT NULL,
 state_json TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE release_approvals (
 digest TEXT PRIMARY KEY,
 actor_json TEXT NOT NULL,
 created_at TEXT NOT NULL
);
