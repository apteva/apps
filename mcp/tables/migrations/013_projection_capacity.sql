-- Separate projection capacity and detailed publication/read timing.
ALTER TABLE projection_definitions ADD COLUMN last_worker_queue_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_read_queue_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_write_lock_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_staging_ms INTEGER NOT NULL DEFAULT 0;
