-- Apteva Code v0.5.11 — split issue lifecycle from workflow status.
--
-- GitHub-style issue lifecycle is state=open|closed plus a close reason.
-- The existing status column is kept as workflow status for board-like
-- values such as todo, in_progress, blocked, and done.

ALTER TABLE repo_issues ADD COLUMN state TEXT NOT NULL DEFAULT 'open';
ALTER TABLE repo_issues ADD COLUMN state_reason TEXT NOT NULL DEFAULT '';

UPDATE repo_issues
   SET state = CASE
       WHEN status IN ('done', 'closed') OR closed_at IS NOT NULL THEN 'closed'
       ELSE 'open'
     END,
       state_reason = CASE
       WHEN status IN ('done', 'closed') OR closed_at IS NOT NULL THEN 'completed'
       ELSE ''
     END,
       status = CASE status
       WHEN 'open' THEN 'todo'
       WHEN 'closed' THEN 'done'
       ELSE status
     END;

CREATE INDEX ix_repo_issues_repo_state ON repo_issues(project_id, repo_id, state, updated_at DESC);
