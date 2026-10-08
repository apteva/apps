-- Additive change capture: NULL retains conservative invalidation for legacy events.
-- New triggers persist the exact dependent versions affected at mutation time.
ALTER TABLE projection_changes ADD COLUMN relevant_projection_ids TEXT;
