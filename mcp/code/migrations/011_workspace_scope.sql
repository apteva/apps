-- Apteva Code v0.8.1 — monorepo-scoped execution workspaces.

ALTER TABLE repo_workspaces ADD COLUMN workspace_patterns_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE repo_workspaces ADD COLUMN support_patterns_json TEXT NOT NULL DEFAULT '[]';
