CREATE TABLE device_policy (
 project_id TEXT PRIMARY KEY, revision INTEGER NOT NULL,
 limits_json TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE device_policy_updates (
 project_id TEXT NOT NULL, request_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
 revision INTEGER NOT NULL, snapshot_json TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,request_id)
);
CREATE TABLE device_http_credentials (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL,
 device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 label TEXT NOT NULL, secret_hash BLOB NOT NULL, secret_cipher TEXT NOT NULL,
 created_at TEXT NOT NULL, expires_at TEXT NOT NULL, recovery_until TEXT NOT NULL,
 revoked_at TEXT, idempotency_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
 UNIQUE(project_id,device_id,idempotency_key)
);
CREATE INDEX device_http_device ON device_http_credentials(project_id,device_id);
CREATE TABLE device_transport_receipts (
 project_id TEXT NOT NULL, message_id TEXT NOT NULL, device_id TEXT NOT NULL,
 outcome TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,message_id)
);
CREATE TABLE device_ingest_rejections (
 project_id TEXT NOT NULL, message_id TEXT NOT NULL, device_id TEXT NOT NULL,
 reason TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,message_id)
);
CREATE TABLE device_usage_coverage (
 project_id TEXT PRIMARY KEY, tracking_since TEXT NOT NULL,
 pruned_before TEXT NOT NULL DEFAULT ''
);
ALTER TABLE device_transport_receipts ADD COLUMN acknowledged_at TEXT;
CREATE INDEX device_transport_prune ON device_transport_receipts(project_id,acknowledged_at);
