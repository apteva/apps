ALTER TABLE release_sync_state ADD COLUMN idle_checks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE release_sync_state ADD COLUMN observed_status TEXT NOT NULL DEFAULT '';
