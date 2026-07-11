-- Unique lease ownership makes completion updates safe under multiple workers.
ALTER TABLE jobs ADD COLUMN lease_token TEXT;

CREATE INDEX ix_jobs_lease ON jobs(status, lease_until);
CREATE INDEX ix_jobs_lease_token ON jobs(project_id, lease_token);
CREATE INDEX ix_jobs_claim ON jobs(project_id, status, next_run_at);
