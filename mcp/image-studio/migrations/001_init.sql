-- Image Studio v0.1 — generations history.

CREATE TABLE generations (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT    NOT NULL,
  prompt          TEXT    NOT NULL,
  revised_prompt  TEXT    DEFAULT '',
  provider        TEXT    NOT NULL,        -- openai-api, replicate, ...
  model           TEXT    DEFAULT '',      -- dall-e-3, gpt-image-1, ...
  size            TEXT    DEFAULT '',
  storage_ids     TEXT    DEFAULT '[]',    -- JSON array of storage file ids when storage is bound
  upstream_urls   TEXT    DEFAULT '[]',    -- JSON array of provider URLs (1hr TTL)
  thumbnail_b64   TEXT    DEFAULT '',      -- base64 of the first image's 256px thumbnail (~30KB)
  count           INTEGER NOT NULL DEFAULT 1,
  created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_generations_project_created
  ON generations(project_id, created_at DESC);
