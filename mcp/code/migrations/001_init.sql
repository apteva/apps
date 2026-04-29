-- Apteva Code v0.1 — repositories metadata.
-- Files live on disk under /data/repos/<slug>/files/ in v0.1.
-- v0.2 migrates files into the Storage app; the schema below is unchanged.

CREATE TABLE repositories (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT    NOT NULL,                  -- Apteva project (APTEVA_PROJECT_ID)
  slug             TEXT    NOT NULL,                  -- url-safe; unique within project
  name             TEXT    NOT NULL,
  description      TEXT    NOT NULL DEFAULT '',
  framework        TEXT    NOT NULL DEFAULT 'blank',  -- blank | nextjs | static | go | python
  storage_root     TEXT    NOT NULL,                  -- "/repos/<slug>/" — virtual mount in the file backend
  owner            TEXT    NOT NULL DEFAULT '',       -- "human:<id>" | "agent:<id>"

  build_cmd        TEXT    NOT NULL DEFAULT '',
  start_cmd        TEXT    NOT NULL DEFAULT '',
  port             INTEGER NOT NULL DEFAULT 0,
  env_json         TEXT    NOT NULL DEFAULT '{}',     -- JSON map; encrypted at rest in v0.2

  deploy_service   TEXT    NOT NULL DEFAULT '',       -- orchestrator service name once deployed
  last_deployed_at TIMESTAMP,

  created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at      TIMESTAMP,

  UNIQUE (project_id, slug)
);
CREATE INDEX ix_repos_proj ON repositories(project_id, archived_at);

-- Audit log for "where did this repo come from?".
CREATE TABLE repo_imports (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL,
  source       TEXT    NOT NULL,                      -- 'zip' | 'github-url' | 'agent-generated' | 'template:<name>'
  imported_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (repo_id) REFERENCES repositories(id)
);
CREATE INDEX ix_imports_repo ON repo_imports(repo_id, imported_at DESC);
