-- Durable ownership survives callbacks, retries, and supervisor restarts.
CREATE TABLE deployment_intents (
 deployment_id INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
 environment_id INTEGER NOT NULL DEFAULT 0,
 desired_state TEXT NOT NULL CHECK(desired_state IN ('running','stopped','archived')),
 release_id INTEGER NOT NULL DEFAULT 0,
 generation INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(deployment_id,environment_id)
);
CREATE TABLE release_runtime (
 release_id INTEGER PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
 process_identity TEXT NOT NULL DEFAULT '',
 config_json TEXT NOT NULL DEFAULT '{}',
 previous_release_id INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE signing_identity_revisions (
 identity_id INTEGER NOT NULL REFERENCES mobile_signing_identities(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 identity_json TEXT NOT NULL,
 encrypted_payload BLOB NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(identity_id,revision)
);
CREATE TABLE ingress_work (
 hostname TEXT PRIMARY KEY,
 project_id TEXT NOT NULL DEFAULT '',
 release_id INTEGER NOT NULL DEFAULT 0,
 target TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 applied_at TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
ALTER TABLE builds ADD COLUMN automatic_release_id INTEGER REFERENCES releases(id) ON DELETE SET NULL;
CREATE INDEX ix_build_release_requested ON builds(status,release_requested);
