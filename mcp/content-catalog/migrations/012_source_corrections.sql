CREATE TABLE asset_source_events (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 asset_id TEXT NOT NULL REFERENCES assets(id),
 operation TEXT NOT NULL CHECK(operation IN ('link','replace')),
 idempotency_key TEXT NOT NULL DEFAULT '',
 request_hash TEXT NOT NULL DEFAULT '',
 previous_revision INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 before_json TEXT NOT NULL,
 after_json TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX ix_source_event_retry ON asset_source_events(project_id,idempotency_key) WHERE idempotency_key<>'';
CREATE INDEX ix_source_event_history ON asset_source_events(project_id,asset_id,created_at);
