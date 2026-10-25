-- First application scans invocation history to build the two partial indexes.
-- Apply with ALL runtimes/producers stopped; see STORAGE_MANAGEMENT.md.
ALTER TABLE function_invocations ADD COLUMN details_expired INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_inv_success_details ON function_invocations(started_at, id)
 WHERE details_expired=0 AND status='ok';
CREATE INDEX ix_inv_failure_details ON function_invocations(started_at, id)
 WHERE details_expired=0 AND status NOT IN ('ok','running');
