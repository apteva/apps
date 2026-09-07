CREATE INDEX IF NOT EXISTS idx_environment_runs_history ON environment_runs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_environment_runs_retention ON environment_runs(stopped_at)
WHERE status IN ('stopped', 'failed', 'expired');
