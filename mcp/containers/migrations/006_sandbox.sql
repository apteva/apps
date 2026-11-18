-- 0.6.0 sandbox: read-only and shared (borrowed, read-only) volume mounts.
ALTER TABLE containers_volumes ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE containers_volumes ADD COLUMN shared_from TEXT NOT NULL DEFAULT '';
