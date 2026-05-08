-- Streaming v0.1 — streams + viewers + lifecycle audit.
--
-- Every table is partitioned by project_id so the same schema serves
-- both `scope: project` (one install per project, project_id is a
-- safety partition) and `scope: global` (one install across projects,
-- project_id is the isolation boundary).

-- One row per allocated stream session.
CREATE TABLE streams (
  id                   INTEGER PRIMARY KEY,
  project_id           TEXT    NOT NULL,

  name                 TEXT    NOT NULL,
  owner_app            TEXT,                        -- "webinars" | "podcasts" | …
  owner_tag            TEXT,                        -- opaque consumer-supplied: "webinar:42"

  -- Ingest + playback identity.
  ingest_protocol      TEXT    NOT NULL DEFAULT 'rtmp',
  ingest_port          INTEGER,                     -- allocated from config range
  stream_key           TEXT    NOT NULL,            -- random secret; auth on ingest
  playback_token       TEXT    NOT NULL,            -- random; gate for signed playback
  visibility           TEXT    NOT NULL DEFAULT 'signed',  -- signed | public

  -- Lifecycle.
  status               TEXT    NOT NULL DEFAULT 'idle',
                                                    -- idle | live | ended | errored
  record               INTEGER NOT NULL DEFAULT 1,
  retention_days       INTEGER NOT NULL DEFAULT 30,
  storage_prefix       TEXT    NOT NULL,            -- relative path under DataDir; "streams/<id>"
  recording_path       TEXT,                        -- path to record.mp4; nullable until finalized

  -- Live metrics (latest scrape from ffmpeg stderr).
  current_bitrate_kbps INTEGER,
  current_fps          REAL,
  resolution           TEXT,                        -- "1920x1080"
  dropped_frames       INTEGER NOT NULL DEFAULT 0,

  -- Viewer aggregates (updated by viewer-counter worker).
  peak_viewers         INTEGER NOT NULL DEFAULT 0,
  total_viewer_seconds INTEGER NOT NULL DEFAULT 0,

  created_at           TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  started_at           TIMESTAMP,                   -- first publisher push
  ended_at             TIMESTAMP,
  error                TEXT
);
CREATE UNIQUE INDEX ux_stream_key   ON streams(stream_key);
CREATE INDEX ix_stream_proj_status  ON streams(project_id, status);
CREATE INDEX ix_stream_owner        ON streams(project_id, owner_app, owner_tag);
CREATE INDEX ix_stream_proj_created ON streams(project_id, created_at DESC);

-- Viewer heartbeats. Anonymous, identified by random cookie. The
-- viewer-counter worker decays rows whose last_heartbeat is older
-- than viewer_idle_seconds and bumps peak_viewers on streams.
CREATE TABLE stream_viewers (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  stream_id       INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
  viewer_id       TEXT NOT NULL,                    -- random per-viewer (cookie)
  external_id     TEXT,                             -- consumer-supplied (webinars registrant token, etc.)
  source          TEXT NOT NULL DEFAULT 'live',     -- live | replay
  joined_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  last_heartbeat  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  left_at         TIMESTAMP,
  watch_seconds   INTEGER NOT NULL DEFAULT 0,
  user_agent      TEXT
);
CREATE UNIQUE INDEX ux_viewer       ON stream_viewers(stream_id, viewer_id);
CREATE INDEX ix_viewer_active       ON stream_viewers(stream_id, last_heartbeat) WHERE left_at IS NULL;
CREATE INDEX ix_viewer_external     ON stream_viewers(stream_id, external_id) WHERE external_id IS NOT NULL;

-- Append-only audit. Every status flip + bitrate-drop + watchdog
-- finding lands here. Mirrors CRM's contact_activities pattern.
CREATE TABLE stream_events (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT NOT NULL,
  stream_id       INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL,
       -- created | started | publisher_disconnect | bitrate_drop |
       -- ended | errored | recording_finalized | key_rotated
  body            TEXT,
  source_detail   TEXT,                             -- JSON
  occurred_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_event_stream ON stream_events(stream_id, occurred_at DESC);
