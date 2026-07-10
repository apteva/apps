-- Cache live row counts so schema lookup and unfiltered counters stay O(1).
-- NULL marks tables created before this migration; the app backfills each
-- count once on first access, then maintains it transactionally.
ALTER TABLE tables_meta ADD COLUMN row_count INTEGER;
