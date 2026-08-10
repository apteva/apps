-- Random daily schedules and logical occurrence identity.
ALTER TABLE jobs ADD COLUMN random_config_json TEXT;
ALTER TABLE jobs ADD COLUMN schedule_seed TEXT;
ALTER TABLE jobs ADD COLUMN scheduled_for TIMESTAMP;

UPDATE jobs SET scheduled_for = next_run_at WHERE scheduled_for IS NULL;

ALTER TABLE job_runs ADD COLUMN scheduled_for TIMESTAMP;
ALTER TABLE job_runs ADD COLUMN idempotency_key TEXT;

CREATE INDEX ix_jobs_scheduled_for ON jobs(project_id, scheduled_for);
CREATE INDEX ix_runs_occurrence ON job_runs(project_id, job_id, scheduled_for);
