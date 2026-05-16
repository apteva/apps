-- Apteva Podcast v0.1.0 — shows + episodes + the RSS feed they back.
--
-- This app owns podcast metadata only. Audio bytes live in the storage
-- app (audio_file_id references a storage file); exact duration + byte
-- length are probed by the media app and cached back onto the episode
-- row so feed generation never blocks on a cross-app call. Download
-- counts are a best-effort local counter — per-event analytics are
-- forwarded to the analytics app when it's installed.
--
-- Ingress wiring (custom feed hostname) doesn't live here: shows.hostname
-- is claimed via routes.routes_register and DNS-wired via domains, the
-- same composition pattern the redirects app uses.

CREATE TABLE shows (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  slug           TEXT    NOT NULL,                       -- feed URL slug, e.g. /feed/my-show.xml
  title          TEXT    NOT NULL,
  description    TEXT    NOT NULL DEFAULT '',
  author         TEXT    NOT NULL DEFAULT '',
  owner_email    TEXT    NOT NULL DEFAULT '',            -- <itunes:owner>; required by Apple at submission
  language       TEXT    NOT NULL DEFAULT 'en',          -- RSS <language>, BCP-47
  category       TEXT    NOT NULL DEFAULT '',            -- iTunes category string
  explicit       INTEGER NOT NULL DEFAULT 0,             -- <itunes:explicit>
  link           TEXT    NOT NULL DEFAULT '',            -- show website URL
  podcast_type   TEXT    NOT NULL DEFAULT 'episodic',    -- 'episodic' | 'serial'
  image_file_id  TEXT    NOT NULL DEFAULT '',            -- storage file id for cover art (1400-3000px square)
  copyright      TEXT    NOT NULL DEFAULT '',
  hostname       TEXT    NOT NULL DEFAULT '',            -- custom feed host; '' = served under platform host
  project_id     TEXT    NOT NULL DEFAULT '',            -- '' for install-scope, otherwise the owning project
  created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(slug, project_id)
);

CREATE INDEX ix_shows_project ON shows(project_id);

CREATE TABLE episodes (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  show_id           INTEGER NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
  guid              TEXT    NOT NULL,                    -- stable RSS <guid>, never reused
  title             TEXT    NOT NULL,
  description       TEXT    NOT NULL DEFAULT '',         -- show notes (HTML)
  season_number     INTEGER,
  episode_number    INTEGER,
  episode_type      TEXT    NOT NULL DEFAULT 'full',     -- 'full' | 'trailer' | 'bonus'
  status            TEXT    NOT NULL DEFAULT 'draft',    -- 'draft' | 'scheduled' | 'published'

  -- Audio. audio_file_id points at a storage file; the rest is the
  -- probe result media hands back, cached so feed gen is a pure read.
  audio_file_id     TEXT    NOT NULL DEFAULT '',
  audio_url         TEXT    NOT NULL DEFAULT '',         -- resolved storage URL, cached for the enclosure
  audio_bytes       INTEGER NOT NULL DEFAULT 0,          -- enclosure length attr — must be exact
  duration_seconds  INTEGER NOT NULL DEFAULT 0,          -- <itunes:duration>
  mime_type         TEXT    NOT NULL DEFAULT 'audio/mpeg',

  image_file_id     TEXT    NOT NULL DEFAULT '',         -- per-episode artwork (optional)
  transcript_file_id TEXT   NOT NULL DEFAULT '',         -- storage file id of transcript from media (optional)

  publish_at        DATETIME,                            -- set when status='scheduled'
  published_at      DATETIME,                            -- set when status flips to 'published'

  downloads         INTEGER NOT NULL DEFAULT 0,          -- best-effort counter; per-event log goes to analytics
  last_download_at  DATETIME,

  created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(guid)
);

CREATE INDEX ix_episodes_show   ON episodes(show_id);
CREATE INDEX ix_episodes_status ON episodes(status);
CREATE INDEX ix_episodes_sched  ON episodes(status, publish_at);
