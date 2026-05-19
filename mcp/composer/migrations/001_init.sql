-- composer v0.1 — multi-clip video compositions + per-render lifecycle.

CREATE TABLE compositions (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id       TEXT    NOT NULL,
  name             TEXT    NOT NULL DEFAULT '',
  edit_json        TEXT    NOT NULL,                  -- canonical Edit JSON (Shotstack-shape subset)
  output_json      TEXT    NOT NULL DEFAULT '{}',     -- {format, resolution, aspect, fps}
  duration_seconds REAL    NOT NULL DEFAULT 0,        -- cached from edit_json clip sum
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_compositions_project ON compositions(project_id, id DESC);

CREATE TABLE renders (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  composition_id      INTEGER NOT NULL,
  project_id          TEXT    NOT NULL,
  executor            TEXT    NOT NULL,                  -- local | remote | shotstack | …
  provider_render_id  TEXT    NOT NULL DEFAULT '',       -- handle for async SaaS executors
  status              TEXT    NOT NULL DEFAULT 'queued', -- queued | rendering | complete | failed | cancelled
  storage_id          INTEGER NOT NULL DEFAULT 0,
  duration_ms         INTEGER NOT NULL DEFAULT 0,
  cost_usd            REAL    NOT NULL DEFAULT 0,
  error               TEXT    NOT NULL DEFAULT '',
  attempts            INTEGER NOT NULL DEFAULT 0,
  edit_snapshot       TEXT    NOT NULL,                  -- frozen edit_json at submit-time
  ffmpeg_command      TEXT    NOT NULL DEFAULT '',       -- captured for debugging local/remote runs
  created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (composition_id) REFERENCES compositions(id) ON DELETE CASCADE
);
CREATE INDEX idx_renders_composition ON renders(composition_id, id DESC);
CREATE INDEX idx_renders_pending
  ON renders(status, updated_at)
  WHERE status IN ('queued', 'rendering');
