-- Durable, ordered change feed for consumers (Media's indexer, catalogs)
-- that previously re-listed the whole inventory to notice changes.
--
-- Triggers write the change row inside the same transaction as the file
-- write, so every path that touches `files` (uploads, moves, renames,
-- checksum repair, soft + hard deletes) is covered without handler code.
-- seq is AUTOINCREMENT: strictly increasing and never reused, so a
-- consumer cursor ("everything after seq N") is stable across restarts.
-- Hard deletes leave a tombstone row (op='deleted') because the file row
-- itself is gone.

CREATE TABLE file_changes (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT    NOT NULL,
  file_id    INTEGER NOT NULL,
  revision   INTEGER NOT NULL DEFAULT 0,
  op         TEXT    NOT NULL,             -- added | updated | deleted
  changed_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX ix_file_changes_project ON file_changes(project_id, seq);
CREATE INDEX ix_file_changes_at ON file_changes(changed_at);

CREATE TRIGGER trg_files_change_insert AFTER INSERT ON files
BEGIN
  INSERT INTO file_changes(project_id, file_id, revision, op)
  VALUES (NEW.project_id, NEW.id, NEW.revision,
          CASE WHEN NEW.deleted_at IS NULL THEN 'added' ELSE 'deleted' END);
END;

-- Only changes a consumer can act on. Checksum bookkeeping and
-- updated_at-only touches do not enter the feed.
CREATE TRIGGER trg_files_change_update AFTER UPDATE ON files
WHEN OLD.project_id IS NOT NEW.project_id
  OR OLD.name IS NOT NEW.name
  OR OLD.folder IS NOT NEW.folder
  OR OLD.sha256 IS NOT NEW.sha256
  OR OLD.size_bytes IS NOT NEW.size_bytes
  OR OLD.content_type IS NOT NEW.content_type
  OR OLD.revision IS NOT NEW.revision
  OR OLD.visibility IS NOT NEW.visibility
  OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
  -- A project move is a delete in the old project and an add in the new.
  INSERT INTO file_changes(project_id, file_id, revision, op)
  SELECT OLD.project_id, OLD.id, OLD.revision, 'deleted'
   WHERE OLD.project_id IS NOT NEW.project_id AND OLD.deleted_at IS NULL;
  INSERT INTO file_changes(project_id, file_id, revision, op)
  VALUES (NEW.project_id, NEW.id, NEW.revision,
          CASE WHEN NEW.deleted_at IS NOT NULL THEN 'deleted'
               WHEN OLD.deleted_at IS NOT NULL OR OLD.project_id IS NOT NEW.project_id THEN 'added'
               ELSE 'updated' END);
END;

CREATE TRIGGER trg_files_change_delete AFTER DELETE ON files
BEGIN
  INSERT INTO file_changes(project_id, file_id, revision, op)
  VALUES (OLD.project_id, OLD.id, OLD.revision, 'deleted');
END;
