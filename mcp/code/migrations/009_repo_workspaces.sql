-- Apteva Code v0.7.0 — durable execution workspaces.
-- Code remains the source/Git authority; this table only records the linked
-- disposable runtime and the last source revision exchanged with it.

CREATE TABLE repo_workspaces (
  project_id        TEXT NOT NULL,
  repo_id           INTEGER NOT NULL,
  workspace_id      TEXT NOT NULL,
  profile           TEXT NOT NULL DEFAULT '',
  source_digest     TEXT NOT NULL DEFAULT '',
  source_paths_json TEXT NOT NULL DEFAULT '[]',
  dependency_digest TEXT NOT NULL DEFAULT '',
  created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, repo_id),
  UNIQUE (workspace_id),
  FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE INDEX ix_repo_workspaces_project
  ON repo_workspaces(project_id, updated_at DESC);
