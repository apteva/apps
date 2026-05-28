-- Calls v0.1 — rooms, unified participants, signaling sessions,
-- media-track metadata, messages, and transcripts.

CREATE TABLE rooms (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  created_by TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at TEXT,
  ended_at TEXT,
  metadata TEXT NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX ux_rooms_slug ON rooms(project_id, slug);
CREATE INDEX ix_rooms_project_status ON rooms(project_id, status);

CREATE TABLE join_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  token TEXT NOT NULL UNIQUE,
  participant_kind TEXT NOT NULL DEFAULT 'human',
  role TEXT NOT NULL DEFAULT 'guest',
  display_name TEXT,
  capabilities TEXT NOT NULL DEFAULT '{}',
  expires_at TEXT,
  max_uses INTEGER DEFAULT 1,
  uses INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_join_tokens_room ON join_tokens(room_id);

CREATE TABLE participants (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_key TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'human',
  role TEXT NOT NULL DEFAULT 'guest',
  display_name TEXT,
  status TEXT NOT NULL DEFAULT 'joining',
  capabilities TEXT NOT NULL DEFAULT '{}',
  joined_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  left_at TEXT,
  last_seen_at TEXT,
  muted_audio INTEGER NOT NULL DEFAULT 0,
  muted_video INTEGER NOT NULL DEFAULT 0,
  metadata TEXT NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX ux_participant_key ON participants(project_id, room_id, participant_key);
CREATE INDEX ix_participants_room ON participants(room_id, status);

CREATE TABLE peer_sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL UNIQUE,
  transport TEXT NOT NULL DEFAULT 'webrtc',
  status TEXT NOT NULL DEFAULT 'negotiating',
  offer_sdp TEXT,
  answer_sdp TEXT,
  ice_state TEXT,
  connection_state TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  connected_at TEXT,
  closed_at TEXT,
  error TEXT
);

CREATE INDEX ix_peer_sessions_room ON peer_sessions(room_id, status);
CREATE INDEX ix_peer_sessions_participant ON peer_sessions(participant_id, status);

CREATE TABLE media_tracks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
  peer_session_id INTEGER REFERENCES peer_sessions(id) ON DELETE SET NULL,
  track_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  source TEXT,
  label TEXT,
  status TEXT NOT NULL DEFAULT 'live',
  started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  ended_at TEXT,
  metadata TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX ix_tracks_room ON media_tracks(room_id, status);

CREATE TABLE room_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id INTEGER REFERENCES participants(id) ON DELETE SET NULL,
  kind TEXT NOT NULL DEFAULT 'chat',
  visibility TEXT NOT NULL DEFAULT 'room',
  body TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_messages_room ON room_messages(room_id, id);

CREATE TABLE transcripts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  room_id INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  participant_id INTEGER REFERENCES participants(id) ON DELETE SET NULL,
  speaker_name TEXT,
  text TEXT NOT NULL,
  started_at_ms INTEGER,
  ended_at_ms INTEGER,
  confidence REAL,
  source TEXT NOT NULL DEFAULT 'audio',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_transcripts_room ON transcripts(room_id, id);
