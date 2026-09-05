-- Dynamic user tables are upgraded transactionally by upgradeTable before use.
ALTER TABLE tables_meta ADD COLUMN storage_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE table_identity (last_id INTEGER NOT NULL);
INSERT INTO table_identity SELECT COALESCE(MAX(id),0) FROM tables_meta;
