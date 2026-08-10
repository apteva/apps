CREATE INDEX IF NOT EXISTS ix_web_runs_status
  ON web_runs(status);

CREATE INDEX IF NOT EXISTS ix_web_runs_created
  ON web_runs(created_at);

CREATE INDEX IF NOT EXISTS ix_web_artifacts_created
  ON web_artifacts(created_at);
