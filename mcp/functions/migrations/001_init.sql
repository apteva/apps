-- Functions v0.1 — Lambda-style serverless functions.
--
-- Two tables: functions (definition) and function_invocations
-- (per-call audit log). Project-partitioned so the same install
-- serves both `scope: project` and `scope: global`.
--
-- source_kind picks where the function body comes from at invoke
-- time:
--   inline — source column holds the raw bytes
--   repo   — repo_id + repo_path point at a file in the Code app's
--            repo; functions resolves it via CallAppResult and
--            caches per source_hash.
-- source_hash is sha256(source bytes); compile cache + temp-dir
-- mount keys off it so we don't re-fetch / re-compile on every
-- invoke.

CREATE TABLE functions (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  name            TEXT    NOT NULL,                     -- slug [a-z0-9-]
  runtime         TEXT    NOT NULL,                     -- bun | node | python | sh
  source_kind     TEXT    NOT NULL DEFAULT 'inline',    -- inline | repo
  source          TEXT,                                  -- inline body
  repo_id         INTEGER,                               -- code app repo id
  repo_path       TEXT,                                  -- entry file path within repo
  source_hash     TEXT    NOT NULL,                     -- sha256 of resolved source bytes
  env_json        TEXT,                                  -- {"K":"v"} merged into spawn env
  timeout_ms      INTEGER NOT NULL DEFAULT 30000,
  max_memory_mb   INTEGER NOT NULL DEFAULT 256,
  status          TEXT    NOT NULL DEFAULT 'active',    -- active | disabled

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

  UNIQUE(project_id, name)
);
CREATE INDEX ix_functions_proj ON functions(project_id, status);

-- Append-only invocation log.
CREATE TABLE function_invocations (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  function_id   INTEGER NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
  started_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at   TIMESTAMP,
  duration_ms   INTEGER,
  status        TEXT    NOT NULL,                       -- ok | error | timeout
  exit_code     INTEGER,
  trigger_kind  TEXT    NOT NULL,                       -- http | manual | event
  event_json    TEXT,                                    -- truncated 4KB
  response_body TEXT,                                    -- truncated 64KB (function stdout)
  stderr        TEXT,                                    -- truncated 16KB
  error         TEXT
);
CREATE INDEX ix_inv_fn   ON function_invocations(function_id, started_at DESC);
CREATE INDEX ix_inv_proj ON function_invocations(project_id, started_at DESC);
