-- Apteva Code v0.5.0 — dev runs.
--
-- One row per (project_id, repo_id). Re-running a repo updates the
-- existing row (status: stopped → starting → live). No history table —
-- dev runs aren't worth preserving past their lifecycle. Logs go to a
-- per-repo file on disk; truncated on each repos_dev_start.
--
-- The supervisor lives inside the code sidecar process. When the
-- sidecar restarts, OnMount runs an orphan-reconciliation pass:
-- rows with status starting|live whose pid is no longer alive get
-- marked stopped with a "supervisor restarted" error message.

CREATE TABLE dev_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT    NOT NULL,
  repo_id       INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  status        TEXT    NOT NULL DEFAULT 'stopped',  -- starting | live | stopped | crashed
  port          INTEGER NOT NULL DEFAULT 0,
  pid           INTEGER NOT NULL DEFAULT 0,
  framework     TEXT    NOT NULL DEFAULT '',
  run_cmd       TEXT    NOT NULL DEFAULT '',         -- override; empty = framework default
  env_json      TEXT    NOT NULL DEFAULT '{}',
  log_path      TEXT    NOT NULL DEFAULT '',
  started_at    TIMESTAMP,
  stopped_at    TIMESTAMP,
  error         TEXT    NOT NULL DEFAULT '',

  UNIQUE(project_id, repo_id)
);

CREATE INDEX ix_dev_runs_status ON dev_runs(status);
