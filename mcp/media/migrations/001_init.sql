-- Media v0.1.

CREATE TABLE media (
  file_id        TEXT    PRIMARY KEY,            -- storage.files.id (string-ified for portability)
  project_id     TEXT    NOT NULL,
  source_sha256  TEXT    NOT NULL,               -- snapshot at probe time; if storage's sha differs, re-probe

  -- Container
  format_name    TEXT,
  duration_ms    INTEGER,
  bitrate        INTEGER,

  -- Stream summary (cached so common filters don't have to crack open a streams table)
  has_video      INTEGER NOT NULL DEFAULT 0,
  has_audio      INTEGER NOT NULL DEFAULT 0,
  is_image       INTEGER NOT NULL DEFAULT 0,

  -- Primary video stream
  width          INTEGER,
  height         INTEGER,
  fps            REAL,
  video_codec    TEXT,

  -- Primary audio stream
  channels       INTEGER,
  sample_rate    INTEGER,
  audio_codec    TEXT,

  -- Probe state machine
  probe_status   TEXT    NOT NULL DEFAULT 'pending', -- pending | ok | failed | unsupported | skipped_size
  probe_error    TEXT    NOT NULL DEFAULT '',
  probe_at       TIMESTAMP,

  -- Full ffprobe JSON. Cheap to keep, future-proofs new fields and
  -- lets power users grep for things we didn't index as columns.
  raw_probe      TEXT    NOT NULL DEFAULT '{}',

  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_media_proj      ON media(project_id, probe_status);
CREATE INDEX ix_media_duration  ON media(project_id, duration_ms) WHERE probe_status='ok';
CREATE INDEX ix_media_codec_v   ON media(project_id, video_codec) WHERE has_video=1;
CREATE INDEX ix_media_codec_a   ON media(project_id, audio_codec) WHERE has_audio=1;

-- Pointers to storage files holding generated artifacts (thumbnail JPEG,
-- waveform PNG, …). The bytes themselves live in storage so signed URLs,
-- visibility, and soft-delete all flow through unchanged.
CREATE TABLE derivations (
  id              INTEGER PRIMARY KEY,
  file_id         TEXT    NOT NULL,              -- source: storage.files.id
  project_id      TEXT    NOT NULL,
  kind            TEXT    NOT NULL,              -- 'thumbnail' | 'waveform' | 'cover'
  storage_file_id TEXT    NOT NULL,              -- result: storage.files.id of the generated artifact
  width           INTEGER,
  height          INTEGER,
  status          TEXT    NOT NULL DEFAULT 'ok', -- ok | failed | stale
  error           TEXT    NOT NULL DEFAULT '',
  generated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(file_id, kind)
);

CREATE INDEX ix_derivations_src ON derivations(file_id, kind);
