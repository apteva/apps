-- Canonical due times; leave scheduled_for untouched because it identifies
-- already-issued idempotency keys for active retries.
UPDATE jobs SET next_run_at = strftime('%Y-%m-%dT%H:%M:%SZ', next_run_at)
 WHERE next_run_at IS NOT NULL;
UPDATE jobs SET lease_until = strftime('%Y-%m-%dT%H:%M:%SZ', lease_until)
 WHERE lease_until IS NOT NULL;
UPDATE jobs SET status='failed', last_error='Schedule has no valid next occurrence'
 WHERE status='pending' AND next_run_at IS NULL;

ALTER TABLE jobs ADD COLUMN parent_job_id INTEGER REFERENCES jobs(id) ON DELETE CASCADE;
ALTER TABLE job_runs ADD COLUMN lease_token TEXT;
ALTER TABLE job_runs ADD COLUMN execution_job_id INTEGER;
CREATE INDEX ix_runs_execution ON job_runs(execution_job_id,status);
CREATE INDEX ix_runs_recent_page ON job_runs(project_id,id DESC);
CREATE INDEX ix_jobs_retention ON jobs(project_id,updated_at,id) WHERE status IN ('done','failed','cancelled');
CREATE UNIQUE INDEX ix_runs_claim ON job_runs(lease_token) WHERE lease_token IS NOT NULL;
CREATE INDEX ix_jobs_parent ON jobs(parent_job_id);
CREATE INDEX ix_jobs_pending ON jobs(project_id,next_run_at,id) WHERE status='pending';
CREATE INDEX ix_jobs_expired ON jobs(project_id,lease_until,id) WHERE status='running';
CREATE INDEX ix_jobs_active_projects ON jobs(status,project_id);
CREATE INDEX ix_jobs_page ON jobs(project_id,id DESC) WHERE parent_job_id IS NULL;
CREATE INDEX ix_jobs_status_page ON jobs(project_id,status,id DESC) WHERE parent_job_id IS NULL;
CREATE INDEX ix_runs_page ON job_runs(project_id,job_id,id DESC);

-- IDs must remain monotonic after optional terminal-job retention.
CREATE TABLE job_sequence (singleton INTEGER PRIMARY KEY CHECK(singleton=1), value INTEGER NOT NULL);
INSERT INTO job_sequence VALUES(1,(SELECT COALESCE(MAX(id),0) FROM jobs));

CREATE INDEX ix_runs_retention ON job_runs(started_at,id) WHERE status <> 'running';
CREATE INDEX ix_jobs_global_retention ON jobs(updated_at,id) WHERE status IN ('done','failed','cancelled');

CREATE TABLE run_sequence (singleton INTEGER PRIMARY KEY CHECK(singleton=1), value INTEGER NOT NULL);
INSERT INTO run_sequence VALUES(1,(SELECT COALESCE(MAX(id),0) FROM job_runs));
