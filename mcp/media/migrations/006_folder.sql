-- Folder column on media so the folder lives on the row, not in a
-- per-call enrichment lookup. Lets media_search filter by folder
-- (and paginate correctly) and media_list_folders aggregate without
-- joining to storage on every call. Populated by upsertMedia at
-- probe time + the storage `file.updated` event handler so renames
-- propagate without a full sweep.
--
-- Default '' (empty) — distinguishes "indexer hasn't filled it
-- yet" from '/' (root). Old rows backfill on the indexer's next
-- probe touch.
ALTER TABLE media ADD COLUMN folder TEXT NOT NULL DEFAULT '';

-- Composite index — most folder queries are project-scoped first,
-- folder-filtered second. SQLite picks this naturally for prefix
-- LIKE queries used by recursive search.
CREATE INDEX ix_media_folder ON media(project_id, folder);
