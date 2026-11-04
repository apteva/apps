-- Candidate grant for audio handover. Written only when softphone_webrtc_handover
-- is on. The active token_hash stays valid until the candidate has connected.
ALTER TABLE telephony_media_sessions ADD COLUMN pending_token_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE telephony_media_sessions ADD COLUMN pending_expires_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telephony_media_sessions ADD COLUMN pending_diagnostic_json TEXT NOT NULL DEFAULT '{}';

-- Project-wide incident reads by time. Existing indexes need a call id.
CREATE INDEX idx_browser_network_project_time ON telephony_browser_network_events(project_id,occurred_at);

-- Results of network checks run during a burst of disconnects. Not tied to one
-- call, so they do not fit telephony_browser_network_events.
CREATE TABLE telephony_audio_probes (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  round_json TEXT NOT NULL
);
CREATE INDEX idx_audio_probes_project_time ON telephony_audio_probes(project_id,occurred_at);
CREATE INDEX idx_audio_probes_expiry ON telephony_audio_probes(expires_at);
