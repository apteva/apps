-- Workflows v0.1 — deterministic, on-demand pipelines.
--
-- Three tables, project-partitioned:
--   workflows               — versioned definition (source + trigger).
--   workflow_runs           — one row per execution.
--   workflow_step_executions — append-only per-step audit log.
--
-- v0.1 is synchronous: an HTTP trigger or workflows_run call walks
-- the step list inline and returns when done. Sidecar restart kills
-- in-flight runs (they show status='running' forever); a sweeper
-- pass on boot marks them failed.

CREATE TABLE workflows (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  name            TEXT    NOT NULL,                       -- slug [a-z0-9-]
  version         INTEGER NOT NULL DEFAULT 1,             -- bumps on each update
  source_kind     TEXT    NOT NULL DEFAULT 'inline',      -- inline | repo
  source          TEXT,                                    -- inline YAML/JSON
  repo_id         INTEGER,                                 -- code app repo id
  repo_path       TEXT,                                    -- entry file path within repo
  source_hash     TEXT    NOT NULL,                       -- sha256 of resolved source bytes

  -- Trigger overrides the in-source trigger when present. Most
  -- workflows declare their trigger inside the YAML, but this
  -- column lets the platform attach an alternate trigger without
  -- editing the source (e.g. a one-off cron sweep).
  trigger_kind    TEXT    NOT NULL DEFAULT 'manual',      -- http | manual | event | schedule
  trigger_json    TEXT,                                    -- topic, schedule, etc.

  status          TEXT    NOT NULL DEFAULT 'active',      -- active | disabled

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

  UNIQUE(project_id, name)
);
CREATE INDEX ix_workflows_proj ON workflows(project_id, status);

CREATE TABLE workflow_runs (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  workflow_id     INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  workflow_name   TEXT    NOT NULL,                       -- denormalised so cascade-deleted defs still surface in listings
  workflow_version INTEGER NOT NULL,                      -- the workflows.version at run-start; replays pin to this

  trigger_kind    TEXT    NOT NULL,                       -- http | manual | event | schedule
  input_json      TEXT,                                    -- frozen at run-start

  status          TEXT    NOT NULL DEFAULT 'pending',    -- pending|running|completed|failed|cancelled
  current_step_id TEXT,                                    -- the step id last attempted; useful for stuck-run forensics
  error           TEXT,                                    -- run-level failure message (truncated 1KB)

  started_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at     TIMESTAMP,
  duration_ms     INTEGER
);
CREATE INDEX ix_runs_workflow ON workflow_runs(workflow_id, started_at DESC);
CREATE INDEX ix_runs_proj     ON workflow_runs(project_id, started_at DESC);
CREATE INDEX ix_runs_status   ON workflow_runs(status);

CREATE TABLE workflow_step_executions (
  id            INTEGER PRIMARY KEY,
  run_id        INTEGER NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  step_id       TEXT    NOT NULL,                         -- the YAML id ("extract", "lookup", …)
  step_kind     TEXT    NOT NULL,                         -- http|function|app|emit|branch
  attempt       INTEGER NOT NULL DEFAULT 1,
  started_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at   TIMESTAMP,
  duration_ms   INTEGER,
  status        TEXT    NOT NULL,                         -- ok|error|skipped|timeout
  input_json    TEXT,                                      -- truncated 16KB
  output_json   TEXT,                                      -- truncated 64KB
  error         TEXT                                       -- truncated 1KB
);
CREATE INDEX ix_steps_run ON workflow_step_executions(run_id, started_at);
