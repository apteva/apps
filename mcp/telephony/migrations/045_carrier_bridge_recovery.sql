ALTER TABLE calls ADD COLUMN media_generation TEXT NOT NULL DEFAULT '';
CREATE TABLE carrier_media_bridges (
 generation TEXT PRIMARY KEY,
 call_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 provider TEXT NOT NULL,
 stream_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'connecting',
 started_at TEXT NOT NULL,
 connected_at TEXT NOT NULL DEFAULT '',
 first_failed_at TEXT NOT NULL DEFAULT '',
 first_failure_json TEXT NOT NULL DEFAULT '{}',
 cleanup_at TEXT NOT NULL DEFAULT '',
 deadline_at TEXT NOT NULL DEFAULT '',
 next_attempt_at TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 events_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX carrier_media_bridges_call ON carrier_media_bridges(project_id,call_id,started_at DESC);
CREATE INDEX carrier_media_bridges_recovery ON carrier_media_bridges(project_id,state,next_attempt_at)
 WHERE state IN ('recovering','connecting');
CREATE INDEX carrier_media_bridges_retention ON carrier_media_bridges(project_id,started_at)
 WHERE state IN ('ended','failed','replaced','disconnected');
