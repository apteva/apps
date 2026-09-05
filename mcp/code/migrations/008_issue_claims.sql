-- Apteva Code v0.6.2 — atomic issue claims.
--
-- Assignment remains long-lived responsibility. A claim records who is
-- actively working the issue and is acquired with one conditional UPDATE so
-- two callers cannot both win.

ALTER TABLE repo_issues ADD COLUMN claim_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_issues ADD COLUMN claim_label TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_issues ADD COLUMN claimed_at TIMESTAMP;

CREATE INDEX ix_repo_issues_claim
  ON repo_issues(project_id, claim_owner, state, updated_at DESC);
