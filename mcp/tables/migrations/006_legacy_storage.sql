-- Additive metadata only: published migration 005 and legacy row bytes stay intact.
ALTER TABLE tables_meta ADD COLUMN legacy_storage INTEGER NOT NULL DEFAULT 0;
CREATE TABLE row_identity (
    table_id INTEGER PRIMARY KEY REFERENCES tables_meta(id) ON DELETE CASCADE,
    last_id INTEGER NOT NULL
);
