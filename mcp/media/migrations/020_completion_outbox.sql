CREATE TABLE media_event_outbox (
 event_id TEXT PRIMARY KEY, project_id TEXT NOT NULL,topic TEXT NOT NULL,payload TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
