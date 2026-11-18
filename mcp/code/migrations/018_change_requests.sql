-- Apteva Code v0.17.0 — native change requests.
--
-- A change request asks to land a native source branch on a target branch.
-- Reviews and comments are notes; protected branches (stored with the native
-- objects, see native_review.go) only move when a request is merged.

CREATE TABLE repo_change_requests (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT    NOT NULL,
  repo_id         INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  number          INTEGER NOT NULL,

  title           TEXT    NOT NULL,
  body            TEXT    NOT NULL DEFAULT '',
  source_branch   TEXT    NOT NULL,
  target_branch   TEXT    NOT NULL,
  state           TEXT    NOT NULL DEFAULT 'open',  -- open | merged | closed
  author          TEXT    NOT NULL DEFAULT '',
  issues          TEXT    NOT NULL DEFAULT '[]',    -- JSON array of issue numbers

  merge_mode      TEXT    NOT NULL DEFAULT '',
  merge_revision  TEXT    NOT NULL DEFAULT '',
  merged_by       TEXT    NOT NULL DEFAULT '',
  merged_at       TIMESTAMP,
  closed_at       TIMESTAMP,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

  UNIQUE(project_id, repo_id, number)
);
CREATE INDEX ix_change_requests_repo_state ON repo_change_requests(project_id, repo_id, state, updated_at DESC);

CREATE TABLE repo_change_request_notes (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  change_request_id  INTEGER NOT NULL REFERENCES repo_change_requests(id) ON DELETE CASCADE,
  kind               TEXT    NOT NULL,                -- comment | review | event
  author             TEXT    NOT NULL DEFAULT '',
  body               TEXT    NOT NULL DEFAULT '',
  verdict            TEXT    NOT NULL DEFAULT '',     -- approve | request_changes | comment (reviews)
  path               TEXT    NOT NULL DEFAULT '',
  line               INTEGER NOT NULL DEFAULT 0,
  revision           TEXT    NOT NULL DEFAULT '',     -- source head when written
  created_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_change_request_notes_cr ON repo_change_request_notes(change_request_id, id);
