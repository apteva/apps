-- 012_keyframes.sql
--
-- Multi-frame "storyboard" support. Until now derivations were one of
-- {thumbnail, waveform} — one row per (file_id, kind). Keyframes are
-- additive: a video gets the canonical thumbnail PLUS N keyframes
-- spaced every keyframe_interval_seconds (default 30s, capped by
-- keyframe_max_count).
--
-- position_ms is 0 for thumbnail/waveform (single-frame derivations)
-- and the source timestamp in milliseconds for keyframes. The
-- per-row UNIQUE constraint moves from (file_id, kind) to
-- (file_id, kind, position_ms) so multiple keyframe rows coexist.
--
-- The existing primary key + indexes stay; this migration only adds
-- the column. The new uniqueness is enforced by an additional
-- partial index.

-- Rebuild derivations to replace UNIQUE(file_id, kind) with
-- UNIQUE(file_id, kind, position_ms). SQLite doesn't support
-- DROP CONSTRAINT — the canonical fix is the copy-rename dance.
-- Done in one statement so it's atomic.

CREATE TABLE derivations_new (
  id              INTEGER PRIMARY KEY,
  file_id         TEXT    NOT NULL,
  project_id      TEXT    NOT NULL,
  kind            TEXT    NOT NULL,                  -- thumbnail | waveform | cover | keyframe
  storage_file_id TEXT    NOT NULL,
  width           INTEGER,
  height          INTEGER,
  position_ms     INTEGER NOT NULL DEFAULT 0,        -- 0 for thumbnail/waveform; source ts in ms for keyframes
  status          TEXT    NOT NULL DEFAULT 'ok',
  error           TEXT    NOT NULL DEFAULT '',
  generated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(file_id, kind, position_ms)
);

INSERT INTO derivations_new
  (id, file_id, project_id, kind, storage_file_id, width, height, position_ms, status, error, generated_at)
SELECT
   id, file_id, project_id, kind, storage_file_id, width, height, 0,           status, error, generated_at
  FROM derivations;

DROP TABLE derivations;
ALTER TABLE derivations_new RENAME TO derivations;

CREATE INDEX IF NOT EXISTS ix_derivations_src
  ON derivations (file_id, kind);

-- New: positional index for keyframe lookups (UI timeline scrub +
-- describer sampling). Partial — keeps the index small since
-- thumbnail/waveform rows always sit at position 0.
CREATE INDEX IF NOT EXISTS ix_derivations_keyframes_pos
  ON derivations (project_id, file_id, position_ms)
  WHERE kind = 'keyframe';
