-- 010_media_name.sql
--
-- Cache the storage filename on the media row so events + tools that
-- want to show "ballerina.MOV (12m14s)" don't have to round-trip to
-- storage. Populated by upsertMedia at probe time from the
-- StorageFile.Name passed in by the indexer; refreshed on every
-- re-index. Empty for legacy rows that predate this column; the
-- next reindex sweep fills them in.

ALTER TABLE media ADD COLUMN name TEXT NOT NULL DEFAULT '';
