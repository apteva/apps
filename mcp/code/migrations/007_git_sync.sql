-- Provider-neutral Git remotes and operation audit. Git refs and the working
-- tree remain authoritative; these tables store connection configuration and
-- the last network-operation outcome for the Code UI.

CREATE TABLE repo_git_remotes (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id       INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  name          TEXT NOT NULL DEFAULT 'origin',
  fetch_url     TEXT NOT NULL,
  push_url      TEXT NOT NULL DEFAULT '',
  connection_id INTEGER NOT NULL DEFAULT 0,
  provider_slug TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT '',
  last_fetch_at TIMESTAMP,
  last_push_at  TIMESTAMP,
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(repo_id, name)
);
CREATE INDEX ix_repo_git_remotes_repo ON repo_git_remotes(repo_id, name);

CREATE TABLE repo_git_operations (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id    INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  operation  TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  from_sha   TEXT NOT NULL DEFAULT '',
  to_sha     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL,
  error      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_repo_git_operations_repo ON repo_git_operations(repo_id, created_at DESC);
