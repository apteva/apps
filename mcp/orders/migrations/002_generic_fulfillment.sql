-- Orders v0.2.3 — generic commerce fulfillment migration marker.
--
-- The SDK migration runner executes multi-statement SQLite files before it
-- records their filename. If a process is interrupted between those steps,
-- retrying ALTER TABLE statements fails on already-added columns. OnMount now
-- reconciles every column and index individually and idempotently, preserving
-- both partially migrated and fresh databases.
SELECT 1;
