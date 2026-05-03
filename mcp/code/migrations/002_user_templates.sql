-- v0.2 — user-defined templates + fork provenance.
-- A template is a regular repo with is_template=1. Forking copies its
-- file tree into a fresh repo. Embedded (build-time) templates remain
-- in the //go:embed FS and are merged in by the lister.

ALTER TABLE repositories ADD COLUMN is_template      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repositories ADD COLUMN template_scope   TEXT;   -- 'private' | 'project' | 'global'
ALTER TABLE repositories ADD COLUMN template_tagline TEXT;
ALTER TABLE repositories ADD COLUMN template_icon    TEXT;
CREATE INDEX ix_repos_template ON repositories(is_template, template_scope);

-- Snapshot string for the parent — the parent repo may later be deleted
-- without breaking provenance lookups.
CREATE TABLE repo_forks (
  child_id     INTEGER PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
  parent_slug  TEXT    NOT NULL,
  parent_kind  TEXT    NOT NULL,            -- 'user' | 'embedded'
  forked_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_forks_parent ON repo_forks(parent_kind, parent_slug);
