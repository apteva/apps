-- v0.4.12: searchable, bounded discovery for existing list tools.
-- FTS tables keep title/name lookup out of the hot todo list queries while
-- retaining the project scope in every result row.

CREATE INDEX IF NOT EXISTS idx_todos_scope_status_title
    ON todos(project_id, status, title COLLATE NOCASE, id);
CREATE INDEX IF NOT EXISTS idx_lists_scope_name
    ON lists(project_id, name COLLATE NOCASE, id);
CREATE INDEX IF NOT EXISTS idx_list_groups_scope_name
    ON list_groups(project_id, name COLLATE NOCASE, id);

CREATE VIRTUAL TABLE IF NOT EXISTS todo_search USING fts5(
    todo_id UNINDEXED,
    project_id UNINDEXED,
    title,
    notes
);

CREATE VIRTUAL TABLE IF NOT EXISTS list_search USING fts5(
    list_id UNINDEXED,
    project_id UNINDEXED,
    name
);

CREATE VIRTUAL TABLE IF NOT EXISTS list_group_search USING fts5(
    group_id UNINDEXED,
    project_id UNINDEXED,
    name
);

INSERT OR REPLACE INTO todo_search(rowid, todo_id, project_id, title, notes)
SELECT id, id, project_id, title, notes FROM todos;
INSERT OR REPLACE INTO list_search(rowid, list_id, project_id, name)
SELECT id, id, project_id, name FROM lists;
INSERT OR REPLACE INTO list_group_search(rowid, group_id, project_id, name)
SELECT id, id, project_id, name FROM list_groups;

CREATE TRIGGER IF NOT EXISTS todo_search_ai AFTER INSERT ON todos BEGIN
    INSERT OR REPLACE INTO todo_search(rowid, todo_id, project_id, title, notes)
    VALUES (new.id, new.id, new.project_id, new.title, new.notes);
END;
CREATE TRIGGER IF NOT EXISTS todo_search_au AFTER UPDATE OF project_id, title, notes ON todos BEGIN
    INSERT OR REPLACE INTO todo_search(rowid, todo_id, project_id, title, notes)
    VALUES (new.id, new.id, new.project_id, new.title, new.notes);
END;
CREATE TRIGGER IF NOT EXISTS todo_search_ad AFTER DELETE ON todos BEGIN
    DELETE FROM todo_search WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS list_search_ai AFTER INSERT ON lists BEGIN
    INSERT OR REPLACE INTO list_search(rowid, list_id, project_id, name)
    VALUES (new.id, new.id, new.project_id, new.name);
END;
CREATE TRIGGER IF NOT EXISTS list_search_au AFTER UPDATE OF project_id, name ON lists BEGIN
    INSERT OR REPLACE INTO list_search(rowid, list_id, project_id, name)
    VALUES (new.id, new.id, new.project_id, new.name);
END;
CREATE TRIGGER IF NOT EXISTS list_search_ad AFTER DELETE ON lists BEGIN
    DELETE FROM list_search WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS list_group_search_ai AFTER INSERT ON list_groups BEGIN
    INSERT OR REPLACE INTO list_group_search(rowid, group_id, project_id, name)
    VALUES (new.id, new.id, new.project_id, new.name);
END;
CREATE TRIGGER IF NOT EXISTS list_group_search_au AFTER UPDATE OF project_id, name ON list_groups BEGIN
    INSERT OR REPLACE INTO list_group_search(rowid, group_id, project_id, name)
    VALUES (new.id, new.id, new.project_id, new.name);
END;
CREATE TRIGGER IF NOT EXISTS list_group_search_ad AFTER DELETE ON list_groups BEGIN
    DELETE FROM list_group_search WHERE rowid = old.id;
END;
