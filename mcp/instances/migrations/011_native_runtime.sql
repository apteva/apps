CREATE TABLE native_artifacts (
 id TEXT PRIMARY KEY, metadata TEXT NOT NULL, fingerprint TEXT NOT NULL,
 bytes INTEGER NOT NULL, sealed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE native_deployments (
 id TEXT PRIMARY KEY, install_id INTEGER NOT NULL, instance_id INTEGER NOT NULL,
 record TEXT NOT NULL
);
CREATE INDEX native_deployments_instance ON native_deployments(instance_id);
