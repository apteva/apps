-- Media v0.2 — render queue for parameterised ffmpeg operations
-- (trim, resize, transcode, concat, crop, extract_frame, audio_extract).
--
-- Lives alongside the existing `media` + `derivations` tables but is
-- driven by a different worker (the render pool) so a long encode
-- can't stall the catalog indexer.

CREATE TABLE renders (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  operation       TEXT    NOT NULL,              -- trim|resize|transcode|concat|crop|extract_frame|audio_extract
  source_file_ids TEXT    NOT NULL,              -- JSON array of storage.files.id (concat takes >1)
  params          TEXT    NOT NULL DEFAULT '{}', -- per-op JSON: {start_ms,end_ms} / {width,height} / ...
  status          TEXT    NOT NULL DEFAULT 'pending', -- pending|running|ok|failed|cancelled
  progress_pct    INTEGER NOT NULL DEFAULT 0,
  output_file_id  TEXT,                          -- storage.files.id once done
  output_name     TEXT,                          -- requested filename (optional)
  error           TEXT    NOT NULL DEFAULT '',
  requested_by    TEXT,                          -- "agent:<id>" | "human:<id>" | "<integration>"

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  started_at      TIMESTAMP,
  completed_at    TIMESTAMP
);

-- Listing renders for a project filtered by status (the panel's
-- queue/running/recent tabs hit this every poll).
CREATE INDEX ix_renders_proj_status ON renders(project_id, status, created_at DESC);

-- Hot path for the worker pool's claim query: pick the oldest pending
-- row regardless of project (workers are install-wide; project scope
-- is enforced at submit time).
CREATE INDEX ix_renders_pending_oldest ON renders(created_at) WHERE status='pending';
