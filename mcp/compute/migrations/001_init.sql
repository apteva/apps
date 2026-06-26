CREATE TABLE compute_jobs (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,

  -- Ownership and scheduling.
  owner_app       TEXT    NOT NULL DEFAULT '',
  owner_ref       TEXT    NOT NULL DEFAULT '',
  idempotency_key TEXT    NOT NULL DEFAULT '',
  priority        INTEGER NOT NULL DEFAULT 50,
  resource_class  TEXT    NOT NULL DEFAULT 'default',

  -- Placement. host_id NULL means "scheduler chooses"; executor is
  -- 'local' for the built-in local runner and 'instances' for the
  -- future remote backend.
  pool            TEXT    NOT NULL DEFAULT '',
  host_id         INTEGER,
  executor        TEXT    NOT NULL DEFAULT 'local',

  -- Work payload. v0.1 supports shell commands. Future revisions can
  -- add container/image specs while keeping the queue lifecycle.
  kind            TEXT    NOT NULL DEFAULT 'shell',
  command         TEXT    NOT NULL,
  cwd             TEXT    NOT NULL DEFAULT '',
  env_json        TEXT    NOT NULL DEFAULT '{}',
  timeout_s       INTEGER NOT NULL DEFAULT 1800,

  -- Runtime state.
  status          TEXT    NOT NULL DEFAULT 'queued',
  attempt         INTEGER NOT NULL DEFAULT 0,
  progress_pct    INTEGER NOT NULL DEFAULT 0,
  output          TEXT    NOT NULL DEFAULT '',
  error           TEXT    NOT NULL DEFAULT '',
  exit_code       INTEGER,

  queued_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  started_at      TIMESTAMP,
  completed_at    TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  cancelled_at    TIMESTAMP
);

CREATE UNIQUE INDEX ix_compute_idempotency
  ON compute_jobs(project_id, idempotency_key)
  WHERE idempotency_key != '';

CREATE INDEX ix_compute_claim
  ON compute_jobs(status, priority, queued_at);

CREATE INDEX ix_compute_project
  ON compute_jobs(project_id, status, queued_at DESC);

CREATE INDEX ix_compute_owner
  ON compute_jobs(project_id, owner_app, status);

CREATE INDEX ix_compute_host
  ON compute_jobs(executor, host_id, status);

