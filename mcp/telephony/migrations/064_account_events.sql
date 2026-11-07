-- Control-plane observations only; no media-loop queries or carrier polling.
CREATE TABLE telephony_account_observations (
 project_id TEXT NOT NULL, connection_id INTEGER NOT NULL, provider TEXT NOT NULL,
 scope TEXT NOT NULL, observed_ns INTEGER NOT NULL,
 PRIMARY KEY(project_id,connection_id,provider,scope)
);
CREATE TABLE telephony_account_issues (
 issue_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, connection_id INTEGER NOT NULL,
 provider TEXT NOT NULL, scope TEXT NOT NULL, operation TEXT NOT NULL, reason TEXT NOT NULL,
 active INTEGER NOT NULL, revision INTEGER NOT NULL, observed_ns INTEGER NOT NULL,
 UNIQUE(project_id,connection_id,provider,scope,reason)
);
CREATE TABLE telephony_account_event_outbox (
 event_id TEXT PRIMARY KEY, issue_id TEXT NOT NULL, revision INTEGER NOT NULL,
 project_id TEXT NOT NULL, topic TEXT NOT NULL, payload_json TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0, due_ms INTEGER NOT NULL DEFAULT 0,
 published_ms INTEGER NOT NULL DEFAULT 0,
 UNIQUE(issue_id,revision)
);
CREATE INDEX idx_account_events_due ON telephony_account_event_outbox(project_id,due_ms) WHERE published_ms=0;
CREATE INDEX idx_account_events_order ON telephony_account_event_outbox(issue_id,revision) WHERE published_ms=0;
CREATE INDEX idx_account_events_retention ON telephony_account_event_outbox(published_ms) WHERE published_ms>0;
