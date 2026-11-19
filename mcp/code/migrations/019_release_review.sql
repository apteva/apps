-- Apteva Code v0.21.0 — human review for Git pull request merges, and the
-- command runs the panel shows.
--
-- An approval is a person's sign-off on one pull request at one head commit.
-- An agent can merge only a pull request a person approved at its current
-- head; a new push to the branch makes earlier approvals stale.

CREATE TABLE repo_git_pr_approvals (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id     INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  number      INTEGER NOT NULL,
  head_sha    TEXT    NOT NULL,
  approved_by TEXT    NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (repo_id, number, head_sha, approved_by)
);
CREATE INDEX ix_repo_git_pr_approvals ON repo_git_pr_approvals(repo_id, number);

-- One row per repos_run_command call (local or workspace), with the tail of
-- its output, so test and build results stay visible after the call.
CREATE TABLE repo_command_runs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id      INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  runtime      TEXT    NOT NULL DEFAULT '',
  workspace_id TEXT    NOT NULL DEFAULT '',
  command      TEXT    NOT NULL,
  status       TEXT    NOT NULL,
  exit_code    INTEGER NOT NULL DEFAULT 0,
  timed_out    INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  actor        TEXT    NOT NULL DEFAULT '',
  output_tail  TEXT    NOT NULL DEFAULT '',
  error        TEXT    NOT NULL DEFAULT '',
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_repo_command_runs_repo ON repo_command_runs(repo_id, id DESC);
