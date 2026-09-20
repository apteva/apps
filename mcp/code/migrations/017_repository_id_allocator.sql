-- Repository ids are durable storage and concurrency identities. SQLite may
-- reuse INTEGER PRIMARY KEY values after the highest row is deleted, so keep a
-- separate monotonic allocator whose value is not affected by repository
-- deletion.
CREATE TABLE repository_id_allocator (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  next_id   INTEGER NOT NULL CHECK (next_id > 0)
);

INSERT INTO repository_id_allocator (singleton, next_id)
SELECT 1, COALESCE(MAX(id), 0) + 1 FROM repositories;
