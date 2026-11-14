-- Delivery acceptance is not a saved ownership checkpoint. Bound legacy
-- rechecks per revision while preserving stable identities for lost replies.
ALTER TABLE process_wait_checks ADD COLUMN attempts INTEGER NOT NULL DEFAULT 1;
CREATE INDEX process_suspended_runtime_holds ON process_step_runs(project_id)
 WHERE state='running' AND delivery_suspended=1 AND retain_worker=0 AND wait_json='';
