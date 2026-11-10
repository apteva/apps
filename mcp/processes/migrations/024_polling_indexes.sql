CREATE INDEX process_due_assignments ON process_assignments(process_id,next_run_at)
 WHERE status='active' AND sync_pending=0 AND next_run_at<>'';
CREATE INDEX process_open_runs ON process_runs(process_id,created_at,id)
 WHERE state NOT IN ('completed','failed','cancelled');
