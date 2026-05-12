-- v0.4: List Groups — a single container layer above lists. Matches
-- Apple Reminders' "List Groups" / Things 3's "Areas" concept: a
-- named, colored bucket that holds related lists. Flat (not nested);
-- a list belongs to at most one group; a group is optional (lists
-- with NULL group_id are "ungrouped", rendered at top of sidebar).
--
-- group_id on lists is ON DELETE SET NULL so deleting a group
-- ungroups its lists rather than cascading the deletion.

CREATE TABLE IF NOT EXISTS list_groups (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    color       TEXT    NOT NULL DEFAULT '#6b7280',
    archived    INTEGER NOT NULL DEFAULT 0,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_list_groups_scope ON list_groups(project_id, archived);

ALTER TABLE lists ADD COLUMN group_id INTEGER REFERENCES list_groups(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_lists_group_id ON lists(group_id);
