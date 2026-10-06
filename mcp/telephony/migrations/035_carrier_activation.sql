ALTER TABLE calls ADD COLUMN carrier_answered_at TEXT NOT NULL DEFAULT '';
-- Only positive carrier/media evidence can establish an existing answered leg.
UPDATE calls SET carrier_answered_at=COALESCE(NULLIF(media_connected_at,''),
 (SELECT MIN(occurred_at) FROM call_events e WHERE e.call_id=calls.id
  AND e.topic='call.answered' AND json_extract(e.payload_json,'$.source')='provider'),'');
CREATE TABLE carrier_activations (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',
 phase TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 deadline_at TEXT NOT NULL,
 next_attempt_at TEXT NOT NULL,
 error_code TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_carrier_activation_pending ON carrier_activations(project_id,status,next_attempt_at);

-- Recovery polls only active AI legs, never a project's historical calls.
CREATE INDEX idx_calls_ai_activation_recovery ON calls(project_id,id)
 WHERE direction='inbound' AND carrier_slug='telnyx' AND peer_kind='realtime'
 AND status IN ('answering','answered','pending');
