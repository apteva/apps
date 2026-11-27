-- notes v0.3: user-created folders.
--
-- Folders start empty; people (or agents) create them. A note sits in at
-- most one folder; folder_id = 0 means "no folder" (shown under All Notes).
-- Deleting a folder moves its notes back to no folder.

CREATE TABLE IF NOT EXISTS folders (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_folders_project_name
    ON folders(project_id, name COLLATE NOCASE);

ALTER TABLE notes ADD COLUMN folder_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_notes_project_folder
    ON notes(project_id, folder_id, updated_at DESC);
