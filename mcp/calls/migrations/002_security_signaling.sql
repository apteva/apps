-- Calls v0.2 — authenticated participants, token revocation, activity,
-- and durable peer-to-peer signaling queues.

ALTER TABLE rooms ADD COLUMN last_activity_at TEXT;
UPDATE rooms SET last_activity_at = COALESCE(ended_at, started_at, created_at);

ALTER TABLE join_tokens ADD COLUMN token_hash TEXT;
ALTER TABLE join_tokens ADD COLUMN revoked_at TEXT;
CREATE UNIQUE INDEX ux_join_tokens_hash ON join_tokens(token_hash) WHERE token_hash IS NOT NULL;
CREATE INDEX ix_join_tokens_expiry ON join_tokens(project_id, expires_at, revoked_at);

CREATE TABLE signaling_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  from_participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
  to_participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  consumed_at TEXT
);

CREATE INDEX ix_signals_recipient
  ON signaling_messages(project_id, room_id, to_participant_id, id);
CREATE INDEX ix_signals_cleanup ON signaling_messages(created_at, consumed_at);

CREATE INDEX ix_participants_presence
  ON participants(project_id, status, last_seen_at);

-- Older heartbeats could append the same browser track more than once. Keep
-- the newest observation so existing installations can adopt the constraint.
DELETE FROM media_tracks
WHERE id NOT IN (
  SELECT MAX(id)
  FROM media_tracks
  GROUP BY participant_id, track_id
);

CREATE UNIQUE INDEX ux_tracks_participant_track
  ON media_tracks(participant_id, track_id);
CREATE INDEX ix_rooms_retention ON rooms(status, ended_at);
