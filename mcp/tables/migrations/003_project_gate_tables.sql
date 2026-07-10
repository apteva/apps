-- A global app installation serves multiple projects from one sidecar,
-- but application data remains project-owned. Normalize legacy table
-- metadata that had a non-empty owner while retaining its project_id.
--
-- Rows created by v0.1.10 with project_id='' are intentionally left
-- untouched and therefore quarantined by the exact-project loaders.
UPDATE tables_meta
SET scope = 'project'
WHERE scope = 'global' AND project_id <> '';
