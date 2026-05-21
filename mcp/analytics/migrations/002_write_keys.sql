-- Analytics v0.4 — public write keys for the static-site tag.
--
-- A write key is a PUBLIC, write-only credential embedded in a website's
-- <script> (like a GA measurement id / PostHog public key). It can only
-- ingest events via GET /collect — never read or query — and is bound to
-- one project + a site slug that every event it ingests is stamped with.
-- Plaintext on purpose: the panel must display it for copy/paste, and it
-- carries no read capability. Defense is origin allowlist + revocation +
-- rate limit, not secrecy.

CREATE TABLE write_keys (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,

  -- The public key value, e.g. "wk_live_<hex>". Unique; looked up on
  -- every /collect hit.
  key             TEXT    NOT NULL UNIQUE,

  -- Site slug stamped as events.app for everything this key ingests,
  -- so a site's traffic groups under one app name.
  site            TEXT    NOT NULL,

  -- Project the key (and its events) belong to. /collect ignores any
  -- client-supplied project and stamps this.
  project_id      TEXT    NOT NULL,

  -- Optional JSON array of allowed Origin hosts. NULL/empty = any
  -- origin. Enforced only when the request actually carries an Origin
  -- (pixel GETs often don't), so it's a best-effort guard, not a wall.
  allowed_origins TEXT,

  created_at      INTEGER NOT NULL,   -- unix ms
  revoked_at      INTEGER,            -- unix ms; NULL = active
  last_used_ts    INTEGER,            -- unix ms of most recent /collect
  event_count     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX ix_write_keys_key     ON write_keys(key);
CREATE INDEX ix_write_keys_project ON write_keys(project_id);
