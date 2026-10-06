-- Projection worker observability and durable queue timing.
ALTER TABLE projection_definitions ADD COLUMN last_queue_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_calculation_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_publication_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_cleanup_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_queue ADD COLUMN queued_at_ms INTEGER NOT NULL DEFAULT 0;

CREATE INDEX projection_generations_gc_idx
  ON projection_generations(projection_id,created_at_ms,generation);
CREATE INDEX projection_queue_scope_due_idx
  ON projection_queue(projection_id,scope_key,due_at_ms,claimed_until);
