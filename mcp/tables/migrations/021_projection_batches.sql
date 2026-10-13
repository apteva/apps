-- Durable follow-up deadlines for projection-wide windows. No result rewrite.
ALTER TABLE projection_queue ADD COLUMN next_due_at_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_queue ADD COLUMN next_queued_at_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN last_batch_started_ms INTEGER NOT NULL DEFAULT 0;
CREATE INDEX projection_batch_fair_idx ON projection_definitions(project_id,last_batch_started_ms,id);
ALTER TABLE projection_changes ADD COLUMN captured_at_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_queue ADD COLUMN batch_cutoff_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_queue ADD COLUMN batch_cutoff_revision INTEGER NOT NULL DEFAULT 0;
-- Stage batch head switches and captured queue revisions outside the final lock.
CREATE TABLE projection_staged_heads (
 projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 scope_key TEXT NOT NULL,
 watermark INTEGER NOT NULL,
 computed_at_ms INTEGER NOT NULL,
 coverage_from TEXT,
 coverage_to TEXT,
 row_count INTEGER NOT NULL,
 result_bytes INTEGER NOT NULL,
 has_head INTEGER NOT NULL,
 queue_revision INTEGER NOT NULL,
 queue_pending INTEGER NOT NULL,
 PRIMARY KEY(projection_id,generation,scope_key)
);
