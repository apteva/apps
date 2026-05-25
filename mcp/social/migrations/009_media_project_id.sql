-- Remember which project owns attached storage media. Social can be
-- installed globally while Storage files remain project-scoped, so
-- publish-time storage calls need this project id as _project_id.

ALTER TABLE posts ADD COLUMN media_project_id TEXT NOT NULL DEFAULT '';
