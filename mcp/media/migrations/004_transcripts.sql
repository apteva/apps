-- Media v0.4 — speech-to-text transcripts for audio + video files.
--
-- Separate table from `media` because:
--   - Transcripts can be MB-sized (text + segments JSON); we don't
--     want to bloat hot media-row reads.
--   - The state machine + provider + cost belong with the
--     transcription concern, not the probe row.
--   - One file may eventually carry transcripts in multiple
--     languages — easy to relax the PRIMARY KEY later (e.g.
--     compound (file_id, language)).
--
-- Lifecycle: row created in `pending` when the worker decides to
-- transcribe. Flipped to `running` while the integration call is in
-- flight, then `ok` (text + segments populated) or `failed`.
-- `skipped` is for files we deliberately don't transcribe (too long,
-- auto-mode disabled, etc.) so the worker doesn't keep picking them
-- up on every sweep.

CREATE TABLE transcripts (
  file_id        TEXT    PRIMARY KEY,            -- storage.files.id, mirrors media.file_id
  project_id     TEXT    NOT NULL,
  source_sha256  TEXT    NOT NULL DEFAULT '',     -- snapshot of media.source_sha256 at transcribe time; mismatch → re-transcribe

  status         TEXT    NOT NULL DEFAULT 'pending', -- pending | running | ok | failed | skipped
  language       TEXT    NOT NULL DEFAULT '',     -- BCP-47 (en, fr-CA), auto-detected when 'auto'
  text           TEXT    NOT NULL DEFAULT '',     -- full plain transcript, no timing
  segments       TEXT    NOT NULL DEFAULT '[]',   -- JSON array: [{start_ms, end_ms, text, speaker?}]

  provider       TEXT    NOT NULL DEFAULT '',     -- 'deepgram' | 'whisper' | 'imported' | 'manual'
  model          TEXT    NOT NULL DEFAULT '',     -- e.g. 'nova-3', 'whisper-large-v3'
  duration_ms    INTEGER,                         -- snapshot of source duration
  cost_cents     REAL,                            -- if integration reports it
  raw            TEXT    NOT NULL DEFAULT '',     -- raw integration response, for debugging
  error          TEXT    NOT NULL DEFAULT '',
  source_kind    TEXT    NOT NULL DEFAULT '',     -- 'auto' | 'manual' | 'imported' — how the row got created

  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  started_at     TIMESTAMP,
  completed_at   TIMESTAMP
);

-- Project-scoped listing + status filtering covers the panel's
-- "queue / running / done" tabs and the transcriber's status sweep.
CREATE INDEX ix_transcripts_proj_status
  ON transcripts(project_id, status, completed_at DESC);

-- Hot path for the worker's claim query: oldest pending row, period.
CREATE INDEX ix_transcripts_pending_oldest
  ON transcripts(created_at)
  WHERE status='pending';
