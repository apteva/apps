-- Apteva Code v0.5.8 — native repo issues.
--
-- Issues are local-first and scoped to a Code repo. GitHub can mirror
-- these later, but ZIP/imported/local repos get the same workflow now.

CREATE TABLE repo_issues (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  repo_id     INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  number      INTEGER NOT NULL,

  title       TEXT    NOT NULL,
  body        TEXT    NOT NULL DEFAULT '',
  type        TEXT    NOT NULL DEFAULT 'task',    -- bug | feature | task | chore
  status      TEXT    NOT NULL DEFAULT 'open',    -- open | triage | planned | in_progress | blocked | done | closed
  priority    TEXT    NOT NULL DEFAULT 'medium',  -- low | medium | high | urgent
  assignee    TEXT    NOT NULL DEFAULT '',
  created_by  TEXT    NOT NULL DEFAULT '',

  closed_at   TIMESTAMP,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

  UNIQUE(project_id, repo_id, number)
);
CREATE INDEX ix_repo_issues_repo_status ON repo_issues(project_id, repo_id, status, updated_at DESC);
CREATE INDEX ix_repo_issues_assignee ON repo_issues(project_id, assignee, status);

CREATE TABLE repo_issue_comments (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id    INTEGER NOT NULL REFERENCES repo_issues(id) ON DELETE CASCADE,
  author      TEXT    NOT NULL DEFAULT '',
  body        TEXT    NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_issue_comments_issue ON repo_issue_comments(issue_id, created_at);

CREATE TABLE repo_issue_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id    INTEGER NOT NULL REFERENCES repo_issues(id) ON DELETE CASCADE,
  event_type  TEXT    NOT NULL,
  actor       TEXT    NOT NULL DEFAULT '',
  data_json   TEXT    NOT NULL DEFAULT '{}',
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_issue_events_issue ON repo_issue_events(issue_id, created_at);

CREATE TABLE repo_issue_links (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id    INTEGER NOT NULL REFERENCES repo_issues(id) ON DELETE CASCADE,
  kind        TEXT    NOT NULL,                    -- path | dev_run | external
  target      TEXT    NOT NULL,
  title       TEXT    NOT NULL DEFAULT '',
  data_json   TEXT    NOT NULL DEFAULT '{}',
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

  UNIQUE(issue_id, kind, target)
);
CREATE INDEX ix_issue_links_issue ON repo_issue_links(issue_id, kind);
