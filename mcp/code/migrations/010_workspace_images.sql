-- Apteva Code v0.8.0 — per-repository and linked-workspace image identity.
-- Workspaces remains responsible for validating and resolving image policy.

ALTER TABLE repositories
  ADD COLUMN workspace_image TEXT NOT NULL DEFAULT '';

ALTER TABLE repo_workspaces
  ADD COLUMN image TEXT NOT NULL DEFAULT '';
