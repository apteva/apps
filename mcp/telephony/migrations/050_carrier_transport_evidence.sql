-- Bounded background observations, kept out of call-list/SSE projections.
CREATE TABLE telephony_carrier_transport_history (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 call_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 evidence_json TEXT NOT NULL
);
CREATE INDEX telephony_carrier_history_call ON telephony_carrier_transport_history(project_id,call_id,kind,occurred_at DESC,id DESC);
CREATE INDEX telephony_carrier_history_expiry ON telephony_carrier_transport_history(expires_at);
CREATE TABLE telephony_carrier_transport_latest (
 project_id TEXT NOT NULL,
 call_id TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 sample_json TEXT NOT NULL,
 PRIMARY KEY(project_id,call_id)
);
CREATE INDEX telephony_carrier_latest_age ON telephony_carrier_transport_latest(occurred_at);
CREATE TRIGGER telephony_carrier_latest_insert AFTER INSERT ON telephony_carrier_transport_latest BEGIN
 INSERT INTO telephony_audio_pending(call_id) VALUES(new.call_id) ON CONFLICT(call_id) DO NOTHING;
END;
CREATE TRIGGER telephony_carrier_latest_update AFTER UPDATE ON telephony_carrier_transport_latest BEGIN
 INSERT INTO telephony_audio_pending(call_id) VALUES(new.call_id) ON CONFLICT(call_id) DO NOTHING;
END;
ALTER TABLE carrier_media_bridges ADD COLUMN proxy_connection_id TEXT NOT NULL DEFAULT '';
ALTER TABLE carrier_media_bridges ADD COLUMN recovery_episode_id TEXT NOT NULL DEFAULT '';
