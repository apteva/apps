-- One durable snapshot per source/upload attempt, independent of the recent
-- polling ring. No old progress is invented or treated as acknowledged.
CREATE TABLE video_delivery_attempts (
 project_id TEXT NOT NULL,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 correlation_id TEXT NOT NULL,
 diagnostics TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(project_id,target_id,correlation_id)
);
