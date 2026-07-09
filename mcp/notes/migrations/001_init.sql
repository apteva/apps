-- notes v0.1: simple searchable text notes for humans and agents.
--
-- Intentionally not a Notion clone: one row per note, simple tags,
-- flexible kind/source fields, soft archive, and project/global scope.

CREATE TABLE IF NOT EXISTS notes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    title         TEXT    NOT NULL,
    body          TEXT    NOT NULL DEFAULT '',
    kind          TEXT    NOT NULL DEFAULT 'note',
    status        TEXT    NOT NULL DEFAULT 'active'
                  CHECK(status IN ('active','archived')),
    source        TEXT    NOT NULL DEFAULT 'manual',
    tags_json     TEXT    NOT NULL DEFAULT '[]',
    metadata_json TEXT    NOT NULL DEFAULT '{}',
    created_by    TEXT    NOT NULL DEFAULT '',
    updated_by    TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    archived_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_notes_project_updated
    ON notes(project_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_notes_project_kind
    ON notes(project_id, kind, updated_at DESC);
