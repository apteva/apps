-- Media Studio v0.3 — generations history (kind-discriminated).

CREATE TABLE generations (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT    NOT NULL,
  kind            TEXT    NOT NULL,        -- image | video | audio_tts | audio_sfx | music
  prompt          TEXT    NOT NULL,
  revised_prompt  TEXT    DEFAULT '',
  provider        TEXT    NOT NULL,        -- app_slug: openai-api, replicate, elevenlabs, suno, ...
  model           TEXT    DEFAULT '',
  storage_ids     TEXT    DEFAULT '[]',    -- JSON []int64
  upstream_urls   TEXT    DEFAULT '[]',    -- JSON []string  (provider URLs, often expiring)
  thumbnail_b64   TEXT    DEFAULT '',      -- images + video posters (~30KB)
  duration_ms     INTEGER DEFAULT 0,       -- video / audio / music
  size            TEXT    DEFAULT '',      -- images
  extra_json      TEXT    DEFAULT '{}',    -- per-kind catch-all: voice_id, aspect, lyrics, ...
  count           INTEGER NOT NULL DEFAULT 1,
  created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_generations_project_kind
  ON generations(project_id, kind, id DESC);
