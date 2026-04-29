-- Jobs v0.1 — scheduler core.
--
-- Two tables: jobs (the schedule) and job_runs (the audit log).
-- Every row is project-partitioned so the same install serves both
-- `scope: project` and `scope: global`. owner_app / owner_instance
-- record who scheduled the job so the UI panel can filter to "this
-- app's jobs" or "this agent's reminders".

CREATE TABLE jobs (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  -- Identity.
  name            TEXT    NOT NULL,                       -- human-readable
  owner_app       TEXT,                                    -- scheduling app slug; empty for human-scheduled
  owner_instance  INTEGER,                                 -- agent instance id when scheduled by an agent

  -- Schedule. exactly one of (cron, every_seconds, run_at) is set.
  schedule_kind   TEXT    NOT NULL,                       -- "once" | "every" | "cron"
  cron_expr       TEXT,                                    -- minute hour dom mon dow (5 fields), or empty
  every_seconds   INTEGER,                                 -- interval in seconds for "every"
  run_at          TIMESTAMP,                               -- absolute fire time for "once"
  timezone        TEXT NOT NULL DEFAULT 'UTC',             -- IANA tz name; cron is evaluated in this tz

  -- Target. tagged-union JSON: {"kind":"http",...} / {"kind":"event",...}.
  target_kind     TEXT    NOT NULL,                       -- "http" | "event"
  target_json     TEXT    NOT NULL,                       -- payload for the target kind

  -- Delivery semantics.
  idempotency_key TEXT,                                    -- caller-supplied; we forward to http targets
  max_retries     INTEGER NOT NULL DEFAULT 3,
  backoff_seconds INTEGER NOT NULL DEFAULT 30,             -- base; exponential with attempt number

  -- Runtime state.
  status          TEXT    NOT NULL DEFAULT 'pending',     -- pending | running | done | failed | cancelled
  next_run_at     TIMESTAMP,                               -- nil when terminal
  last_run_at     TIMESTAMP,
  last_status     TEXT,                                    -- ok | error
  last_error      TEXT,                                    -- truncated to ~1KB
  attempt         INTEGER NOT NULL DEFAULT 0,              -- attempts on the *current* fire

  -- Optimistic-lock lease. Set to now()+lease_ttl when the dispatcher
  -- picks up a row; used to prevent two ticks from both running the
  -- same job if the previous tick crashed mid-flight.
  lease_until     TIMESTAMP,

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  cancelled_at    TIMESTAMP
);
CREATE INDEX ix_jobs_due       ON jobs(status, next_run_at);
CREATE INDEX ix_jobs_proj      ON jobs(project_id, status, next_run_at);
CREATE INDEX ix_jobs_owner_app ON jobs(project_id, owner_app, status);
CREATE INDEX ix_jobs_owner_inst ON jobs(project_id, owner_instance, status);

-- Append-only run log. One row per dispatch attempt — including the
-- retries — so the panel can show "tried 3 times, last error: timeout".
CREATE TABLE job_runs (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  job_id        INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  started_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at   TIMESTAMP,
  duration_ms   INTEGER,
  status        TEXT    NOT NULL,                         -- ok | error | timeout
  http_status   INTEGER,                                   -- when target is http
  response_body TEXT,                                      -- truncated; first 2KB
  error         TEXT,                                      -- truncated; first 1KB
  attempt       INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_runs_job  ON job_runs(job_id, started_at DESC);
CREATE INDEX ix_runs_proj ON job_runs(project_id, started_at DESC);
