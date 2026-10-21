ALTER TABLE post_targets ADD COLUMN delivery_diagnostics TEXT NOT NULL DEFAULT '{}';
CREATE TABLE delivery_recovery_requests (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 post_id INTEGER NOT NULL,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 source_attempt INTEGER NOT NULL,
 fingerprint TEXT NOT NULL,
 decision TEXT NOT NULL,
 evidence TEXT NOT NULL,
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 expires_at TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'prepared',
 result TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX delivery_recovery_target ON delivery_recovery_requests(target_id,revision,status);
CREATE UNIQUE INDEX delivery_recovery_once ON delivery_recovery_requests(target_id,revision) WHERE status IN ('consumed','complete') AND decision IN ('retry','resume');
